package compress

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "meshsat/internal/compress/compresspb"
)

// LlamaZipClient wraps the gRPC connection to the llama-zip sidecar.
type LlamaZipClient struct {
	addr    string
	conn    *grpc.ClientConn
	client  pb.CompressionServiceClient
	mu      sync.RWMutex
	ready   bool
	timeout time.Duration
}

// NewLlamaZipClient creates a client for the llama-zip gRPC sidecar.
// It does not connect immediately — call Connect() or let it connect lazily.
func NewLlamaZipClient(addr string, timeout time.Duration) *LlamaZipClient {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &LlamaZipClient{
		addr:    addr,
		timeout: timeout,
	}
}

// Connect establishes the gRPC connection and checks health.
func (c *LlamaZipClient) Connect(ctx context.Context) error {
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
		return fmt.Errorf("llamazip: dial %s: %w", c.addr, err)
	}

	c.conn = conn
	c.client = pb.NewCompressionServiceClient(conn)

	// Health check
	healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := c.client.Health(healthCtx, &pb.HealthRequest{})
	if err != nil {
		c.conn.Close()
		c.conn = nil
		c.client = nil
		return fmt.Errorf("llamazip: health check failed: %w", err)
	}

	c.ready = resp.Ready
	log.Info().
		Str("addr", c.addr).
		Str("model", resp.ModelName).
		Str("device", resp.Device).
		Int64("model_mb", resp.ModelSizeBytes/1024/1024).
		Bool("ready", resp.Ready).
		Msg("llamazip: connected to sidecar")

	return nil
}

// Close closes the gRPC connection.
func (c *LlamaZipClient) Close() error {
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
func (c *LlamaZipClient) IsReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ready && c.client != nil
}

// Compress sends data to the llama-zip sidecar for compression.
func (c *LlamaZipClient) Compress(data []byte) ([]byte, int, error) {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()

	if client == nil {
		return nil, 0, fmt.Errorf("llamazip: not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	resp, err := client.Compress(ctx, &pb.CompressRequest{Data: data})
	if err != nil {
		return nil, 0, fmt.Errorf("llamazip: compress: %w", err)
	}

	return resp.Compressed, int(resp.DurationMs), nil
}

// Decompress sends data to the llama-zip sidecar for decompression.
func (c *LlamaZipClient) Decompress(data []byte) ([]byte, int, error) {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()

	if client == nil {
		return nil, 0, fmt.Errorf("llamazip: not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	resp, err := client.Decompress(ctx, &pb.DecompressRequest{Compressed: data})
	if err != nil {
		return nil, 0, fmt.Errorf("llamazip: decompress: %w", err)
	}

	return resp.Data, int(resp.DurationMs), nil
}
