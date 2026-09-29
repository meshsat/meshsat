package hubreporter

import (
	"encoding/hex"
	"math"
	"testing"
	"time"
)

// MeshSat Android's alarm test sends SosMessages.positionFrame(bridgeId, fix,
// altitudeM, nowSec) through its modem, and its doc says the frame is byte
// for byte this encoder's. The expected bytes are the Kotlin algorithm's:
// the first vector written out by hand from positionFrame (ByteBuffer is
// big-endian, Double.toFloat rounds to nearest, the height is
// altitudeM.toFloat().toInt() coerced into the int16 range, source 1, the
// time putInt), the other two the vectors Android's own SosMessagesTest
// asserts on its implementation. [MESHSAT-1430]
func TestEncodeSatPosition_IsAndroidsPositionFrame(t *testing.T) {
	at := time.Unix(1790000000, 0)
	for _, tc := range []struct {
		name     string
		lat, lon float64
		alt      float32
		want     string
	}{
		{
			name: "by hand: msa-flaneur, 52.1601, 4.4970, 3.9 m",
			lat:  52.1601, lon: 4.4970, alt: 3.9,
			want: "4d53" + "01" + "01" + // magic "MS", version 1, type 1 (position)
				"0b" + "6d73612d666c616e657572" + // 11 bytes of "msa-flaneur"
				"4250a3f1" + // float32(52.1601): exponent 132, mantissa round(0.630003125 * 2^23) = 0x50A3F1
				"408fe76d" + // float32(4.4970): exponent 129, mantissa round(0.12425 * 2^23) = 0x0FE76D
				"0003" + // 3.9 m cut toward zero
				"01" + // source: GPS
				"6ab13b80", // 1790000000
		},
		{
			name: "Android's test vector, 52.1620671, 4.5097402, 12.7 m",
			lat:  52.1620671, lon: 4.5097402, alt: 12.7,
			want: "4d5301010b6d73612d666c616e6575724250a5f540904fcb000c016ab13b80",
		},
		{
			name: "Android's test vector, -33.86882, 151.20929, -3.2 m",
			lat:  -33.86882, lon: 151.20929, alt: -3.2,
			want: "4d5301010b6d73612d666c616e657572c20779ac43173594fffd016ab13b80",
		},
	} {
		got := hex.EncodeToString(EncodeSatPosition("msa-flaneur", tc.lat, tc.lon, tc.alt, 1, at))
		if got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// The height is held to what the frame's int16 carries, as Android's
// coerceIn holds it; a bare int16(float32) wrapped instead (40000 m went out
// as -25536). NaN is 0, as Kotlin's Float.toInt makes it. [MESHSAT-1430]
func TestEncodeSatPosition_HeightHeldToInt16(t *testing.T) {
	for _, tc := range []struct {
		alt  float32
		want int16
	}{
		{3.9, 3},
		{-3.2, -3},
		{-0.7, 0},
		{8848.9, 8848},
		{32767, 32767},
		{32767.9, 32767},
		{40000, 32767},
		{1e10, 32767},
		{float32(math.Inf(1)), 32767},
		{-32768, -32768},
		{-32768.5, -32768},
		{-40000, -32768},
		{float32(math.Inf(-1)), -32768},
		{float32(math.NaN()), 0},
	} {
		frame := EncodeSatPosition("b", 1, 1, tc.alt, 1, time.Unix(0, 0))
		hdr, payload, err := DecodeSatUplink(frame)
		if err != nil || hdr.MsgType != SatMsgPosition {
			t.Fatalf("alt %v: header %+v, %v", tc.alt, hdr, err)
		}
		_, _, _, alt, _, _, err := DecodeSatPosition(payload)
		if err != nil {
			t.Fatalf("alt %v: %v", tc.alt, err)
		}
		if alt != float32(tc.want) {
			t.Errorf("alt %v went out as %v, want %d", tc.alt, alt, tc.want)
		}
	}
}
