package shared

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"collective/pkg/protocol"
	"google.golang.org/grpc"
)

const (
	// StreamChunkSize is the size of each streaming chunk (3MB to stay under 4MB gRPC limit)
	StreamChunkSize = 3 * 1024 * 1024
)

// StreamingWriter handles large file uploads using gRPC streaming
type StreamingWriter struct {
	client protocol.CoordinatorClient
	conn   *grpc.ClientConn
}

// NewStreamingWriter creates a new streaming writer
func NewStreamingWriter(client protocol.CoordinatorClient, conn *grpc.ClientConn) *StreamingWriter {
	return &StreamingWriter{
		client: client,
		conn:   conn,
	}
}

// WriteFile streams a file from a reader to the coordinator
func (sw *StreamingWriter) WriteFile(ctx context.Context, path string, reader io.Reader, totalSize int64) error {
	// Create the streaming connection
	stream, err := sw.client.WriteFileStream(ctx)
	if err != nil {
		return fmt.Errorf("failed to create write stream: %w", err)
	}

	// Send header with file metadata
	err = stream.Send(&protocol.WriteFileStreamRequest{
		Data: &protocol.WriteFileStreamRequest_Header{
			Header: &protocol.WriteFileStreamHeader{
				Path:      path,
				TotalSize: totalSize,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to send header: %w", err)
	}

	// Stream the file data in chunks
	buffer := make([]byte, StreamChunkSize)
	chunkIndex := int32(0)

	for {
		n, err := reader.Read(buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read data: %w", err)
		}

		// Send chunk
		err = stream.Send(&protocol.WriteFileStreamRequest{
			Data: &protocol.WriteFileStreamRequest_ChunkData{
				ChunkData: buffer[:n],
			},
		})
		if err != nil {
			return fmt.Errorf("failed to send chunk %d: %w", chunkIndex, err)
		}

		chunkIndex++
	}

	// Stream is complete - no need to send final empty chunk with this protocol

	// Close and get response
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return fmt.Errorf("failed to receive response: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("streaming write failed: %s", resp.Message)
	}

	return nil
}

// WriteFileFromData writes data directly using streaming
func (sw *StreamingWriter) WriteFileFromData(ctx context.Context, path string, data []byte) error {
	reader := bytes.NewReader(data)
	return sw.WriteFile(ctx, path, reader, int64(len(data)))
}
