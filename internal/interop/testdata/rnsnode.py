#!/usr/bin/env python3
"""Generic Reticulum peer for the MeshSat interop tests.

Runs under the pinned venv (internal/interop/rnsenv). Reads one JSON command
per line on stdin, writes one JSON event per line on stdout. Everything is
upstream RNS/LXMF behaviour; nothing here knows about MeshSat.

Args:
  --config-dir DIR      Reticulum config/storage directory (created)
  --transport yes|no    enable_transport
  --connect HOST:PORT   TCPClientInterface to a server
  --listen PORT         TCPServerInterface on 127.0.0.1
  --name NAME           LXMF display name (default "rnsnode")
  --stamp-cost N        inbound stamp cost announced (0 = none)
  --enforce-stamps      enforce inbound stamps

Commands (JSON objects):
  {"cmd":"announce"}                       announce the LXMF delivery destination
  {"cmd":"request_path","dest":HEX}        Transport.request_path
  {"cmd":"has_path","dest":HEX}            -> {"event":"path","dest":...,"known":bool,"hops":n,"next_hop":HEX}
  {"cmd":"link","dest":HEX}                open an RNS.Link to dest (must be known)
  {"cmd":"link_send","link":HEX,"data":TEXT}  send data on a link, report proof
  {"cmd":"link_close","link":HEX}
  {"cmd":"send_lxm","dest":HEX,"content":TEXT,"title":TEXT,"method":"opportunistic|direct"}
  {"cmd":"send_packet","dest":HEX,"data":TEXT}  single encrypted packet to a SINGLE dest, waits for proof
  {"cmd":"set_keepalive","link":HEX,"seconds":N}
  {"cmd":"status"}
  {"cmd":"quit"}

Events: {"event":"ready","identity":HEX,"delivery":HEX,"transport_id":HEX},
  announce, path, link_established, link_closed, link_packet, link_proof,
  lxm_delivered, lxm_failed, lxm_received, packet_proved, packet_received, error.
"""
import argparse, json, os, sys, threading, time

import RNS
import LXMF
from LXMF import LXStamper

# Stamp generation in one process: the multiprocess generator starts one
# worker per core, which on a shared test host is both slow to spawn and
# unwelcome; the stamps it produces are the same.
LXStamper.job_linux = LXStamper.job_simple
LXStamper.job_linux_managed = LXStamper.job_simple

out_lock = threading.Lock()


def emit(**ev):
    with out_lock:
        sys.stdout.write(json.dumps(ev) + "\n")
        sys.stdout.flush()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--config-dir", required=True)
    ap.add_argument("--transport", default="no")
    ap.add_argument("--connect", default=None)
    ap.add_argument("--listen", default=None)
    ap.add_argument("--name", default="rnsnode")
    ap.add_argument("--stamp-cost", type=int, default=0)
    ap.add_argument("--enforce-stamps", action="store_true")
    ap.add_argument("--loglevel", type=int, default=2)
    ap.add_argument("--keep-config", action="store_true", help="use the config already in --config-dir")
    args = ap.parse_args()

    os.makedirs(args.config_dir, exist_ok=True)
    ifaces = ""
    if args.connect:
        host, port = args.connect.rsplit(":", 1)
        ifaces += f"""
  [[client]]
    type = TCPClientInterface
    enabled = yes
    target_host = {host}
    target_port = {port}
"""
    if args.listen:
        ifaces += f"""
  [[server]]
    type = TCPServerInterface
    enabled = yes
    listen_ip = 127.0.0.1
    listen_port = {args.listen}
"""
    if args.keep_config: pass
    else:
      with open(os.path.join(args.config_dir, "config"), "w") as f:
        f.write(f"""[reticulum]
  enable_transport = {"Yes" if args.transport == "yes" else "No"}
  share_instance = No
  panic_on_interface_error = No

[logging]
  loglevel = {args.loglevel}

[interfaces]
{ifaces}
""")

    RNS.logfile = os.path.join(args.config_dir, "rns.log")
    RNS.logdest = RNS.LOG_FILE
    reticulum = RNS.Reticulum(args.config_dir)
    identity = RNS.Identity()
    router = LXMF.LXMRouter(identity=identity, storagepath=args.config_dir, enforce_stamps=args.enforce_stamps)
    stamp_cost = args.stamp_cost if args.stamp_cost > 0 else None
    delivery = router.register_delivery_identity(identity, display_name=args.name, stamp_cost=stamp_cost)

    def on_lxm(message):
        emit(event="lxm_received", hash=message.hash.hex(), source=message.source_hash.hex(),
             content=message.content.decode("utf-8", "replace"), title=message.title.decode("utf-8", "replace"),
             signature_validated=bool(message.signature_validated), stamp_valid=bool(getattr(message, "stamp_valid", False)),
             method=str(message.method))

    router.register_delivery_callback(on_lxm)

    # A plain SINGLE destination for raw packet tests, separate from LXMF.
    raw_dest = RNS.Destination(identity, RNS.Destination.IN, RNS.Destination.SINGLE, "interop", "raw")
    raw_dest.set_proof_strategy(RNS.Destination.PROVE_ALL)
    raw_dest.accepts_links(True)

    def on_raw_packet(data, packet):
        emit(event="packet_received", data=data.decode("utf-8", "replace"), hash=packet.packet_hash.hex(), hops=packet.hops)

    raw_dest.set_packet_callback(on_raw_packet)

    links = {}

    def on_link_established(link):
        links[link.link_id.hex()] = link
        emit(event="link_established", link=link.link_id.hex(), dest=link.destination.hash.hex() if link.destination else raw_dest.hash.hex(),
             rtt=link.rtt, initiator=bool(link.initiator), mtu=link.mtu)
        link.set_link_closed_callback(lambda l: emit(event="link_closed", link=l.link_id.hex(), reason=l.teardown_reason))
        link.set_packet_callback(lambda data, packet: emit(event="link_packet", link=link.link_id.hex(), data=data.decode("utf-8", "replace")))
        link.set_remote_identified_callback(lambda l, ident: emit(event="link_identified", link=l.link_id.hex(), identity=ident.hash.hex()))

    raw_dest.set_link_established_callback(on_link_established)

    class AnnounceHandler:
        aspect_filter = None
        receive_path_responses = True  # RNS skips path responses for handlers without this

        def received_announce(self, destination_hash, announced_identity, app_data, announce_packet_hash=None):
            emit(event="announce", dest=destination_hash.hex(), identity=announced_identity.hash.hex(),
                 app_data=(app_data.hex() if app_data else ""), hops=RNS.Transport.hops_to(destination_hash))

    RNS.Transport.register_announce_handler(AnnounceHandler())

    emit(event="ready", identity=identity.hash.hex(), delivery=delivery.hash.hex(), raw=raw_dest.hash.hex(),
         transport_id=RNS.Transport.identity.hash.hex(), rns=RNS.__version__, lxmf=LXMF.__version__)

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            cmd = json.loads(line)
        except Exception as e:
            emit(event="error", error=f"bad json: {e}")
            continue
        c = cmd.get("cmd")
        try:
            if c == "quit":
                break
            elif c == "announce":
                delivery.announce()
                raw_dest.announce()
                emit(event="announced", delivery=delivery.hash.hex(), raw=raw_dest.hash.hex())
            elif c == "request_path":
                RNS.Transport.request_path(bytes.fromhex(cmd["dest"]))
                emit(event="path_requested", dest=cmd["dest"])
            elif c == "has_path":
                d = bytes.fromhex(cmd["dest"])
                known = RNS.Transport.has_path(d)
                nh = RNS.Transport.next_hop(d)
                emit(event="path", dest=cmd["dest"], known=bool(known), hops=RNS.Transport.hops_to(d) if known else -1,
                     next_hop=nh.hex() if nh else "")
            elif c == "link":
                d = bytes.fromhex(cmd["dest"])
                ident = RNS.Identity.recall(d)
                if ident is None:
                    emit(event="error", error="destination identity unknown")
                    continue
                dest = RNS.Destination(ident, RNS.Destination.OUT, RNS.Destination.SINGLE, "interop", "raw")
                dest.hash = d
                dest.hexhash = d.hex()
                link = RNS.Link(dest, established_callback=on_link_established,
                                closed_callback=lambda l: emit(event="link_closed", link=l.link_id.hex(), reason=l.teardown_reason))
                links[link.link_id.hex()] = link
                emit(event="link_requested", link=link.link_id.hex())
            elif c == "link_send":
                link = links[cmd["link"]]
                p = RNS.Packet(link, cmd["data"].encode("utf-8"))
                receipt = p.send()
                if receipt:
                    receipt.set_delivery_callback(lambda r: emit(event="link_proof", link=cmd["link"], rtt=r.get_rtt()))
                    receipt.set_timeout_callback(lambda r: emit(event="link_proof_timeout", link=cmd["link"]))
            elif c == "link_identify":
                links[cmd["link"]].identify(identity)
                emit(event="identified", link=cmd["link"])
            elif c == "link_close":
                links[cmd["link"]].teardown()
            elif c == "set_keepalive":
                link = links[cmd["link"]]
                link.keepalive = cmd["seconds"]
                link.stale_time = 2 * cmd["seconds"]
                emit(event="keepalive_set", link=cmd["link"])
            elif c == "send_packet":
                d = bytes.fromhex(cmd["dest"])
                ident = RNS.Identity.recall(d)
                dest = RNS.Destination(ident, RNS.Destination.OUT, RNS.Destination.SINGLE, "interop", "raw")
                dest.hash = d
                dest.hexhash = d.hex()
                p = RNS.Packet(dest, cmd["data"].encode("utf-8"))
                receipt = p.send()
                receipt.set_delivery_callback(lambda r: emit(event="packet_proved", hash=r.hash.hex(), rtt=r.get_rtt()))
                receipt.set_timeout_callback(lambda r: emit(event="packet_timeout", hash=r.hash.hex()))
                emit(event="packet_sent", hash=p.packet_hash.hex())
            elif c == "send_lxm":
                d = bytes.fromhex(cmd["dest"])
                ident = RNS.Identity.recall(d)
                if ident is None:
                    emit(event="error", error="destination identity unknown")
                    continue
                dest = RNS.Destination(ident, RNS.Destination.OUT, RNS.Destination.SINGLE, "lxmf", "delivery")
                method = LXMF.LXMessage.DIRECT if cmd.get("method") == "direct" else LXMF.LXMessage.OPPORTUNISTIC
                lxm = LXMF.LXMessage(dest, delivery, cmd.get("content", "").encode("utf-8"),
                                     title=cmd.get("title", "").encode("utf-8"), desired_method=method)
                lxm.register_delivery_callback(lambda m: emit(event="lxm_delivered", hash=m.hash.hex()))
                lxm.register_failed_callback(lambda m: emit(event="lxm_failed", hash=m.hash.hex(), state=m.state))
                router.handle_outbound(lxm)
                emit(event="lxm_sent", hash=lxm.hash.hex(), method=str(lxm.method))
            elif c == "status":
                emit(event="status", paths=len(RNS.Transport.path_table), links=len(links))
            else:
                emit(event="error", error=f"unknown command {c}")
        except Exception as e:
            emit(event="error", error=f"{type(e).__name__}: {e}")

    try:
        router.exit_handler()
    except Exception:
        pass
    os._exit(0)


if __name__ == "__main__":
    main()
