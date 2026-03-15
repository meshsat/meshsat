package compress

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "meshsat/internal/compress/msvqscpb"
)

// mockMSVQSCServer implements a simple echo-back encoder for testing.
type mockMSVQSCServer struct {
	pb.UnimplementedMSVQSCServiceServer
}

func (m *mockMSVQSCServer) Encode(_ context.Context, req *pb.EncodeRequest) (*pb.EncodeResponse, error) {
	stages := int32(4)
	if req.MaxStages > 0 && req.MaxStages < stages {
		stages = req.MaxStages
	}
	// Mock wire format: 1 header byte + 2 bytes per stage
	wire := make([]byte, 1+int(stages)*2)
	wire[0] = byte((stages&0x0F)<<4 | 1) // stages + version 1
	return &pb.EncodeResponse{
		Encoded:          wire,
		StagesUsed:       stages,
		EstimatedFidelity: 0.95,
		OriginalSize:     int32(len(req.Data)),
		DurationMs:       1,
	}, nil
}

func (m *mockMSVQSCServer) Decode(_ context.Context, req *pb.DecodeRequest) (*pb.DecodeResponse, error) {
	// Mock: return a fixed reconstructed message
	return &pb.DecodeResponse{
		Data:       []byte("reconstructed message"),
		StagesUsed: 4,
		DurationMs: 1,
	}, nil
}

func (m *mockMSVQSCServer) Health(_ context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{
		Ready:           true,
		EncoderModel:    "test-model",
		CodebookStages:  8,
		CodebookEntries: 1024,
		EmbeddingDim:    384,
		CorpusSize:      45,
		Device:          "cpu",
	}, nil
}

func startMockMSVQSCServer(t *testing.T) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	pb.RegisterMSVQSCServiceServer(srv, &mockMSVQSCServer{})
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func TestMSVQSCClient_ConnectAndEncode(t *testing.T) {
	addr, stop := startMockMSVQSCServer(t)
	defer stop()

	client := NewMSVQSCClient(addr, 5*time.Second)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if !client.IsReady() {
		t.Fatal("expected client to be ready after connect")
	}

	// Test encode
	input := []byte("Battery 78%, signal strong. GPS fix 3D.")
	encoded, stages, fidelity, err := client.Encode(input, 0)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if stages < 1 {
		t.Errorf("expected at least 1 stage, got %d", stages)
	}
	if fidelity < 0 || fidelity > 1.0 {
		t.Errorf("expected fidelity in [0,1], got %f", fidelity)
	}
	if len(encoded) == 0 {
		t.Error("expected non-empty encoded output")
	}

	// Test decode
	decoded, decStages, err := client.Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decStages < 1 {
		t.Errorf("expected at least 1 stage on decode, got %d", decStages)
	}
	if len(decoded) == 0 {
		t.Error("expected non-empty decoded output")
	}
}

func TestMSVQSCClient_RateAdaptive(t *testing.T) {
	addr, stop := startMockMSVQSCServer(t)
	defer stop()

	client := NewMSVQSCClient(addr, 5*time.Second)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	input := []byte("SOS SOS SOS. Hiker injured at summit.")

	// Encode with 2 stages (ZigBee)
	enc2, stages2, _, err := client.Encode(input, 2)
	if err != nil {
		t.Fatalf("encode 2 stages: %v", err)
	}
	if stages2 != 2 {
		t.Errorf("expected 2 stages, got %d", stages2)
	}

	// Encode with 6 stages (Iridium)
	enc6, stages6, _, err := client.Encode(input, 6)
	if err != nil {
		t.Fatalf("encode 6 stages: %v", err)
	}

	// More stages = larger wire
	if len(enc6) <= len(enc2) {
		t.Errorf("expected 6-stage encoding (%d) to be larger than 2-stage (%d)",
			len(enc6), len(enc2))
	}
	_ = stages6
}

func TestMSVQSCClient_NotConnected(t *testing.T) {
	client := NewMSVQSCClient("localhost:0", 1*time.Second)
	if client.IsReady() {
		t.Fatal("expected client to not be ready before connect")
	}
	_, _, _, err := client.Encode([]byte("test"), 0)
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	_, _, err = client.Decode([]byte{0x41, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestMSVQSCClient_ConnectFail(t *testing.T) {
	client := NewMSVQSCClient("127.0.0.1:1", 1*time.Second)
	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected connect to fail on closed port")
	}
}

func TestMSVQSCClient_EmptyData(t *testing.T) {
	addr, stop := startMockMSVQSCServer(t)
	defer stop()

	client := NewMSVQSCClient(addr, 5*time.Second)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	encoded, _, _, err := client.Encode([]byte{}, 0)
	if err != nil {
		t.Fatalf("encode empty: %v", err)
	}
	// Mock returns a wire header even for empty input
	if len(encoded) == 0 {
		t.Error("expected wire header even for empty input")
	}
}

func TestNewMSVQSCClient_DefaultTimeout(t *testing.T) {
	client := NewMSVQSCClient("localhost:50052", 0)
	if client.timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", client.timeout)
	}
}

func TestMSVQSCClient_DirectGRPC(t *testing.T) {
	addr, stop := startMockMSVQSCServer(t)
	defer stop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := pb.NewMSVQSCServiceClient(conn)

	health, err := client.Health(context.Background(), &pb.HealthRequest{})
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !health.Ready {
		t.Error("expected ready=true")
	}
	if health.EncoderModel != "test-model" {
		t.Errorf("expected encoder 'test-model', got %q", health.EncoderModel)
	}
	if health.CodebookStages != 8 {
		t.Errorf("expected 8 stages, got %d", health.CodebookStages)
	}
	if health.CodebookEntries != 1024 {
		t.Errorf("expected K=1024, got %d", health.CodebookEntries)
	}
}
