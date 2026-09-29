package main

import (
	"encoding/base64"
	"fmt"
	"time"

	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
	"meshsat/internal/types"
)

// hubUplinkSatInterfaces are the satellite interfaces a Hub uplink frame may
// leave on, newest modem first.
var hubUplinkSatInterfaces = []string{"iridium_imt_0", "iridium_0"}

// hubUplinkSatInterface picks the satellite interface for a Hub uplink frame
// and says whether it is worth using right now: connected, and it has moved
// traffic in the last 30 minutes (indoors it has not, and a frame queued on it
// would never leave, so "auto" falls to SMS). The interface is returned even
// when it is not usable, so a forced "satellite" policy queues onto the modem
// the kit really has. With no satellite gateway at all it names the first
// candidate and the delivery fails loudly in the ledger. [MESHSAT-963]
func hubUplinkSatInterface(status func(id string) (connected bool, lastActivity time.Time, exists bool), now time.Time) (string, bool) {
	first := ""
	for _, id := range hubUplinkSatInterfaces {
		connected, last, exists := status(id)
		if !exists {
			continue
		}
		if first == "" {
			first = id
		}
		if connected && !last.IsZero() && now.Sub(last) < 30*time.Minute {
			return id, true
		}
	}
	if first == "" {
		first = hubUplinkSatInterfaces[0]
	}
	return first, false
}

// hubUplinkFrameOptions is how a Hub uplink frame is queued, on either
// bearer: precedence Priority, class hub_uplink (no egress rules, no
// transform chain), and the SOS frame alone at priority 0 (Critical). The
// SOS then goes before every other row of its precedence, an alarm test
// waiting on the same modem included, no arrival evicts it, and the SBD
// gateway's credit budget never holds it back. The periodic position and
// health frames stay at priority 1, under the budget. [MESHSAT-1430,
// MESHSAT-1431]
func hubUplinkFrameOptions(frame []byte) engine.DirectSendOptions {
	return engine.DirectSendOptions{
		Precedence: string(types.PrecedencePriority),
		Class:      database.DeliveryClassHubUplink,
		Critical:   hubreporter.IsSatSOS(frame),
	}
}

// hubUplinkSender is the satellite fallback's SendFn: it queues a frame for
// the Hub on the satellite or, failing that, as SMS to the Hub's number.
//
// Bearer choice: "satellite" and "sms" force one leg; "auto" takes the
// satellite gateway when it is connected and has moved traffic in the last
// 30 min (indoors it has not, and a queued satellite frame would never
// leave), else SMS to the Hub's number. The satellite interface is
// whichever modem this kit carries. It was hardcoded to iridium_0 (9603),
// so on a 9704 kit, which both are since 20 Sep 2026, "auto" could only
// ever pick SMS and "satellite" queued onto an interface that does not
// exist. [MESHSAT-963]
func hubUplinkSender(dispatcher *engine.Dispatcher, status func(id string) (connected bool, lastActivity time.Time, exists bool), bearerPolicy, hubSMS string) func(frame []byte) error {
	return func(frame []byte) error {
		satIface, satOK := hubUplinkSatInterface(status, time.Now())
		useSat := satOK
		switch bearerPolicy {
		case "satellite":
			useSat = true
		case "sms":
			useSat = false
		}
		opts := hubUplinkFrameOptions(frame)
		label := fmt.Sprintf("hub uplink frame, %d B", len(frame))
		if useSat {
			opts.Payload = frame
			_, _, err := dispatcher.QueueDirectSendTo(satIface, label, opts)
			return err
		}
		if hubSMS == "" {
			return fmt.Errorf("hub uplink: no satellite in reach and no Hub SMS number configured")
		}
		opts.Destination = hubSMS
		_, _, err := dispatcher.QueueDirectSendTo("cellular_0", base64.StdEncoding.EncodeToString(frame), opts)
		return err
	}
}

// hubUplinkSOSSender is the satellite fallback's SOSSendFn: an SOS frame by
// SMS to the Hub's number, where the bearer choice takes SMS ("sms", or
// "auto" without a satellite in reach), under the SOS's reference, so the
// SOS's cancellation and its send gate stop it. The satellite is not its
// to take: the SOS queues its satellite leg itself, on the modem this kit
// has, waiting for it when it is not there (api sosTrySatellite). With the
// policy "satellite" it sends nothing. [MESHSAT-1446]
func hubUplinkSOSSender(dispatcher *engine.Dispatcher, status func(id string) (connected bool, lastActivity time.Time, exists bool), bearerPolicy, hubSMS string) func(frame []byte, msgRef string) error {
	return func(frame []byte, msgRef string) error {
		_, satOK := hubUplinkSatInterface(status, time.Now())
		useSMS := !satOK
		switch bearerPolicy {
		case "satellite":
			useSMS = false
		case "sms":
			useSMS = true
		}
		if !useSMS {
			return nil
		}
		if hubSMS == "" {
			return fmt.Errorf("hub uplink: no satellite in reach and no Hub SMS number configured")
		}
		opts := hubUplinkFrameOptions(frame)
		opts.Precedence = string(types.PrecedenceOverride)
		opts.Destination = hubSMS
		opts.MsgRef = msgRef
		_, _, err := dispatcher.QueueDirectSendTo("cellular_0", base64.StdEncoding.EncodeToString(frame), opts)
		return err
	}
}

// hubUplinkGatewayStatus reads a satellite link's state for the Hub uplink's
// bearer choice: connected, its last traffic, and whether it runs at all.
func hubUplinkGatewayStatus(gwMgr interface {
	GatewayByInterfaceID(id string) gateway.Gateway
}) func(id string) (bool, time.Time, bool) {
	return func(id string) (bool, time.Time, bool) {
		gw := gwMgr.GatewayByInterfaceID(id)
		if gw == nil {
			return false, time.Time{}, false
		}
		st := gw.Status()
		return st.Connected, st.LastActivity, true
	}
}
