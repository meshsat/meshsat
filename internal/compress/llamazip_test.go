package compress

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "meshsat/internal/compress/compresspb"
)

// mockCompressionServer implements a simple echo-back compressor for testing.
type mockCompressionServer struct {
	pb.UnimplementedCompressionServiceServer
}

func (m *mockCompressionServer) Compress(_ context.Context, req *pb.CompressRequest) (*pb.CompressResponse, error) {
	// Mock: just return the data as-is (no real compression)
	return &pb.CompressResponse{
		Compressed:   req.Data,
		OriginalSize: int32(len(req.Data)),
		DurationMs:   1,
	}, nil
}

func (m *mockCompressionServer) Decompress(_ context.Context, req *pb.DecompressRequest) (*pb.DecompressResponse, error) {
	return &pb.DecompressResponse{
		Data:       req.Compressed,
		DurationMs: 1,
	}, nil
}

func (m *mockCompressionServer) Health(_ context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{
		Ready:          true,
		ModelName:      "test-model",
		ModelSizeBytes: 1024,
		Device:         "cpu",
	}, nil
}

func startMockServer(t *testing.T) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	pb.RegisterCompressionServiceServer(srv, &mockCompressionServer{})
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() { srv.Stop() }
}

func TestLlamaZipClient_ConnectAndCompress(t *testing.T) {
	addr, stop := startMockServer(t)
	defer stop()

	client := NewLlamaZipClient(addr, 5*time.Second)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if !client.IsReady() {
		t.Fatal("expected client to be ready after connect")
	}

	// Test compress round-trip
	input := []byte("Battery 78%, signal strong. GPS fix 3D.")
	compressed, durMs, err := client.Compress(input)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if durMs < 0 {
		t.Errorf("expected non-negative duration, got %d", durMs)
	}

	decompressed, _, err := client.Decompress(compressed)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(decompressed) != string(input) {
		t.Errorf("round-trip failed: got %q, want %q", decompressed, input)
	}
}

func TestLlamaZipClient_NotConnected(t *testing.T) {
	client := NewLlamaZipClient("localhost:0", 1*time.Second)
	if client.IsReady() {
		t.Fatal("expected client to not be ready before connect")
	}
	_, _, err := client.Compress([]byte("test"))
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestLlamaZipClient_ConnectFail(t *testing.T) {
	// Use a port that nothing listens on
	client := NewLlamaZipClient("127.0.0.1:1", 1*time.Second)
	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected connect to fail on closed port")
	}
}

func TestLlamaZipClient_EmptyData(t *testing.T) {
	addr, stop := startMockServer(t)
	defer stop()

	client := NewLlamaZipClient(addr, 5*time.Second)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Empty compress should work (mock returns empty)
	compressed, _, err := client.Compress([]byte{})
	if err != nil {
		t.Fatalf("compress empty: %v", err)
	}
	if len(compressed) != 0 {
		t.Errorf("expected empty result for empty input, got %d bytes", len(compressed))
	}
}

func TestNewLlamaZipClient_DefaultTimeout(t *testing.T) {
	client := NewLlamaZipClient("localhost:50051", 0)
	if client.timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", client.timeout)
	}
}

// TestLlamaZipClient_DirectGRPC verifies the generated proto code works.
func TestLlamaZipClient_DirectGRPC(t *testing.T) {
	addr, stop := startMockServer(t)
	defer stop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := pb.NewCompressionServiceClient(conn)

	// Health
	health, err := client.Health(context.Background(), &pb.HealthRequest{})
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !health.Ready {
		t.Error("expected ready=true")
	}
	if health.ModelName != "test-model" {
		t.Errorf("expected model name 'test-model', got %q", health.ModelName)
	}
}
