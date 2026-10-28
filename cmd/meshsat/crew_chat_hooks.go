package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/crewchat"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
	"meshsat/internal/types"
)

// Crew chat over satellite and SMS (MESHSAT-1744). A dispatcher's chat the
// Hub could not hand this kit over MQTT comes as one frame (crewchat C1):
// a satellite MT, or base64 in an SMS from the Hub's number. The answer and
// the kit's word on the chat go back as uplink frames (C2, C3) on the same
// bearer through the delivery ledger, class hub_uplink, as the satellite
// fallback's own frames do (hub_uplink_iface.go).

// crewChatDeliverer is the part of gateway.TAKChat the inbound reader needs.
type crewChatDeliverer interface {
	DeliverFrame(f crewchat.Chat, bearer string) int
}

// crewChatInbound is the processor's crew chat reader
// (engine.Processor.SetCrewChatInbound): a C1 frame on a satellite link is
// the MT's bytes; on the cellular link it is base64 in an SMS, and only the
// Hub's own number may send one, since the frame carries no signature. A
// true return consumes the text; anything else passes through untouched.
func crewChatInbound(chat crewChatDeliverer, hubSMS string) func(ctx context.Context, ifaceID, fromAddr, text string) bool {
	return func(_ context.Context, ifaceID, fromAddr, text string) bool {
		var raw []byte
		bearer := ""
		switch {
		case strings.HasPrefix(ifaceID, "iridium_imt"):
			raw, bearer = []byte(text), "imt"
		case strings.HasPrefix(ifaceID, "iridium"):
			raw, bearer = []byte(text), "sbd"
		case strings.HasPrefix(ifaceID, "cellular"):
			if hubSMS == "" || fromAddr != hubSMS {
				return false
			}
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
			if err != nil {
				return false
			}
			raw, bearer = b, "sms"
		default:
			return false
		}
		if !crewchat.IsChat(raw) {
			return false
		}
		f, err := crewchat.DecodeChat(raw)
		if err != nil {
			log.Warn().Err(err).Str("iface", ifaceID).Str("from", fromAddr).Int("bytes", len(raw)).
				Msg("tak chat: a crew chat frame could not be read")
			return true
		}
		chat.DeliverFrame(f, bearer)
		return true
	}
}

// crewChatFrameSender queues a C2 or C3 frame for the Hub on the bearer the
// chat came on: raw on the satellite interface the bearer names (the other
// satellite interface when that one is not running, so a kit that swapped
// modems still answers), base64 in an SMS to the Hub's number for "sms".
// Class hub_uplink, precedence Priority, as the fallback's frames.
func crewChatFrameSender(dispatcher *engine.Dispatcher, status func(id string) (connected bool, lastActivity time.Time, exists bool), hubSMS string) func(bearer string, frame []byte) error {
	return func(bearer string, frame []byte) error {
		opts := engine.DirectSendOptions{Precedence: string(types.PrecedencePriority), Class: database.DeliveryClassHubUplink}
		label := fmt.Sprintf("crew chat frame, %d B", len(frame))
		switch bearer {
		case "sms":
			if hubSMS == "" {
				return fmt.Errorf("crew chat: no Hub SMS number configured")
			}
			opts.Destination = hubSMS
			_, _, err := dispatcher.QueueDirectSendTo("cellular_0", base64.StdEncoding.EncodeToString(frame), opts)
			return err
		case "sbd", "imt":
			iface := "iridium_0"
			if bearer == "imt" {
				iface = "iridium_imt_0"
			}
			if _, _, exists := status(iface); !exists {
				if other, _ := hubUplinkSatInterface(status, time.Now()); other != "" {
					iface = other
				}
			}
			opts.Payload = frame
			_, _, err := dispatcher.QueueDirectSendTo(iface, label, opts)
			return err
		}
		return fmt.Errorf("crew chat: %q is not a bearer a frame goes on", bearer)
	}
}

// crewChatFrames are gateway.TAKChat's ways back over satellite and SMS.
func crewChatFrames(send func(bearer string, frame []byte) error, hubUp func() bool) *gateway.TAKChatFrames {
	return &gateway.TAKChatFrames{SendAnswer: send, SendStatus: send, HubUp: hubUp}
}
