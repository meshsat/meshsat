package compress

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "meshsat/internal/compress/msvqscpb"
)

// MSVQSCClient wraps the gRPC connection to the MSVQ-SC sidecar.
// Provides rate-adaptive lossy semantic compression via multi-stage
// residual vector quantization.
type MSVQSCClient struct {
	addr    string
	conn    *grpc.ClientConn
	client  pb.MSVQSCServiceClient
	mu      sync.RWMutex
	ready   bool
	timeout time.Duration
}

// NewMSVQSCClient creates a client for the MSVQ-SC gRPC sidecar.
// It does not connect immediately — call Connect() explicitly.
func NewMSVQSCClient(addr string, timeout time.Duration) *MSVQSCClient {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &MSVQSCClient{
		addr:    addr,
		timeout: timeout,
	}
}

// Connect establishes the gRPC connection and checks health.
func (c *MSVQSCClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil
	}

	conn, err := grpc.NewClient(
		c.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(1*1024*1024),
			grpc.MaxCallSendMsgSize(1*1024*1024),
		),
	)
	if err != nil {
		return fmt.Errorf("msvqsc: dial %s: %w", c.addr, err)
	}

	c.conn = conn
	c.client = pb.NewMSVQSCServiceClient(conn)

	// Health check
	healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := c.client.Health(healthCtx, &pb.HealthRequest{})
	if err != nil {
		c.conn.Close()
		c.conn = nil
		c.client = nil
		return fmt.Errorf("msvqsc: health check failed: %w", err)
	}

	c.ready = resp.Ready
	log.Info().
		Str("addr", c.addr).
		Str("encoder", resp.EncoderModel).
		Int32("stages", resp.CodebookStages).
		Int32("K", resp.CodebookEntries).
		Int32("dim", resp.EmbeddingDim).
		Int32("corpus", resp.CorpusSize).
		Str("device", resp.Device).
		Bool("ready", resp.Ready).
		Msg("msvqsc: connected to sidecar")

	return nil
}

// Close closes the gRPC connection.
func (c *MSVQSCClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		c.client = nil
		c.ready = false
		return err
	}
	return nil
}

// IsReady returns true if the sidecar is connected and healthy.
func (c *MSVQSCClient) IsReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ready && c.client != nil
}

// Encode sends data to the MSVQ-SC sidecar for lossy semantic compression.
// maxStages controls the rate (fewer stages = more compression, lower fidelity).
// Returns: encoded wire bytes, stages used, estimated fidelity (cosine sim), error.
func (c *MSVQSCClient) Encode(data []byte, maxStages int) ([]byte, int, float32, error) {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()

	if client == nil {
		return nil, 0, 0, fmt.Errorf("msvqsc: not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	resp, err := client.Encode(ctx, &pb.EncodeRequest{
		Data:      data,
		MaxStages: int32(maxStages),
	})
	if err != nil {
		return nil, 0, 0, fmt.Errorf("msvqsc: encode: %w", err)
	}

	return resp.Encoded, int(resp.StagesUsed), resp.EstimatedFidelity, nil
}

// Decode sends encoded wire data to the MSVQ-SC sidecar for reconstruction.
// Returns: reconstructed text bytes, stages used, error.
func (c *MSVQSCClient) Decode(data []byte) ([]byte, int, error) {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()

	if client == nil {
		return nil, 0, fmt.Errorf("msvqsc: not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	resp, err := client.Decode(ctx, &pb.DecodeRequest{Encoded: data})
	if err != nil {
		return nil, 0, fmt.Errorf("msvqsc: decode: %w", err)
	}

	return resp.Data, int(resp.StagesUsed), nil
}
