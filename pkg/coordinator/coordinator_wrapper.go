package coordinator

import (
	"collective/pkg/protocol"
	"go.uber.org/zap"
)

// WriteFileStream is the gRPC interface implementation that chooses the appropriate handler
func (c *Coordinator) WriteFileStream(stream protocol.Coordinator_WriteFileStreamServer) error {
	if c.useOptimizedStreaming {
		c.logger.Debug("Using optimized streaming for write")
		return c.WriteFileStreamOptimized(stream)
	}
	
	c.logger.Debug("Using standard streaming for write")
	return c.WriteFileStreamStandard(stream)
}

// ReadFileStream is the gRPC interface implementation that chooses the appropriate handler
func (c *Coordinator) ReadFileStream(req *protocol.ReadFileStreamRequest, stream protocol.Coordinator_ReadFileStreamServer) error {
	if c.useOptimizedStreaming {
		c.logger.Debug("Using optimized streaming for read",
			zap.String("path", req.Path))
		return c.ReadFileStreamOptimized(req, stream)
	}
	
	c.logger.Debug("Using standard streaming for read",
		zap.String("path", req.Path))
	return c.ReadFileStreamStandard(req, stream)
}