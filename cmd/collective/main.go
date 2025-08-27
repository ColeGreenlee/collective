package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"collective/pkg/config"
	"collective/pkg/coordinator"
	collectivefuse "collective/pkg/fuse"
	"collective/pkg/node"
	"collective/pkg/protocol"
	"collective/pkg/utils"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
)

var (
	configFile string
	verbose    bool
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "collective",
		Short: "Distributed storage collective system",
		Long: `A federated hub-and-spoke distributed storage system for small trusted groups.
Each member runs a coordinator that manages their storage nodes.`,
	}

	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "config file path")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")

	rootCmd.AddCommand(
		coordinatorCmd(),
		nodeCmd(),
		clientCmd(),
		peerCmd(),
		versionCmd(),
		enhancedStatusCmd(),  // Use enhanced status command
		mountCmd(),
		mkdirCmd(),
		lsCmd(),
		rmCmd(),
		mvCmd(),
		cleanupCmd,
		authCommand(),
		initCommand(),
		configCommand(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func coordinatorCmd() *cobra.Command {
	var (
		memberID       string
		address        string
		bootstrapPeers []string
		dataDir        string
	)

	cmd := &cobra.Command{
		Use:   "coordinator",
		Short: "Run in coordinator mode",
		Long:  `Start a coordinator that manages nodes and peers with other coordinators.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(verbose)
			defer logger.Sync()

			// Load configuration
			var cfg *config.Config
			if configFile != "" {
				var err error
				cfg, err = config.LoadConfig(configFile)
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}
			} else {
				// Use flags or environment variables
				cfg = &config.Config{
					Mode:     config.ModeCoordinator,
					MemberID: memberID,
					Coordinator: config.CoordinatorConfig{
						Address: address,
						DataDir: dataDir,
					},
				}
				
				// Parse bootstrap peers
				for _, peer := range bootstrapPeers {
					// Format: memberID:address
					parts := strings.SplitN(peer, ":", 2)
					if len(parts) != 2 {
						return fmt.Errorf("invalid bootstrap peer format: %s (expected memberID:address)", peer)
					}
					cfg.Coordinator.BootstrapPeers = append(cfg.Coordinator.BootstrapPeers, config.PeerConfig{
						MemberID: parts[0],
						Address:  parts[1],
					})
				}
			}

			if cfg.MemberID == "" {
				return fmt.Errorf("member ID is required")
			}

			// Create and start coordinator
			var coord *coordinator.Coordinator
			if cfg.Auth != nil && cfg.Auth.Enabled {
				logger.Info("Starting coordinator with authentication enabled")
				coord = coordinator.NewWithAuth(&cfg.Coordinator, cfg.MemberID, logger, cfg.Auth)
			} else {
				coord = coordinator.New(&cfg.Coordinator, cfg.MemberID, logger)
			}
			
			// Connect to bootstrap peers with retry
			for _, peer := range cfg.Coordinator.BootstrapPeers {
				peerCopy := peer // Capture loop variable
				go func() {
					retryCount := 0
					baseDelay := time.Second
					maxDelay := time.Minute * 5
					
					for {
						logger.Info("Attempting to connect to bootstrap peer", 
							zap.String("member_id", peerCopy.MemberID),
							zap.String("address", peerCopy.Address),
							zap.Int("attempt", retryCount+1))
						
						if err := coord.ConnectToPeer(peerCopy.MemberID, peerCopy.Address); err != nil {
							retryCount++
							delay := baseDelay * time.Duration(1<<uint(min(retryCount-1, 10)))
							if delay > maxDelay {
								delay = maxDelay
							}
							
							logger.Warn("Failed to connect to bootstrap peer, will retry", 
								zap.String("member_id", peerCopy.MemberID),
								zap.String("address", peerCopy.Address),
								zap.Error(err),
								zap.Duration("retry_in", delay))
							
							time.Sleep(delay)
						} else {
							logger.Info("Successfully connected to bootstrap peer",
								zap.String("member_id", peerCopy.MemberID),
								zap.String("address", peerCopy.Address))
							break
						}
					}
				}()
			}

			// Handle shutdown gracefully
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

			go func() {
				<-sigChan
				logger.Info("Shutting down coordinator")
				coord.Stop()
				os.Exit(0)
			}()

			logger.Info("Starting coordinator",
				zap.String("member_id", cfg.MemberID),
				zap.String("address", cfg.Coordinator.Address))

			return coord.Start()
		},
	}

	cmd.Flags().StringVar(&memberID, "member-id", "", "unique member identifier")
	cmd.Flags().StringVar(&address, "address", ":8001", "coordinator listening address")
	cmd.Flags().StringSliceVar(&bootstrapPeers, "bootstrap-peers", nil, "initial peer addresses to connect to")
	cmd.Flags().StringVar(&dataDir, "data-dir", "./data", "directory for storing data")

	return cmd
}

func nodeCmd() *cobra.Command {
	var (
		memberID           string
		nodeID             string
		address            string
		coordinatorAddress string
		capacityStr        string
		dataDir            string
	)

	cmd := &cobra.Command{
		Use:   "node",
		Short: "Run in storage node mode",
		Long:  `Start a storage node that registers with a coordinator and stores data chunks.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(verbose)
			defer logger.Sync()

			// Parse capacity from human-friendly format
			var capacity int64
			if capacityStr != "" {
				var err error
				capacity, err = utils.ParseDataSize(capacityStr)
				if err != nil {
					return fmt.Errorf("invalid capacity format: %w", err)
				}
			} else {
				capacity = 1073741824 // Default 1GB
			}

			// Load configuration
			var cfg *config.Config
			if configFile != "" {
				var err error
				cfg, err = config.LoadConfig(configFile)
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}
			} else {
				// Use flags or environment variables
				cfg = &config.Config{
					Mode:     config.ModeNode,
					MemberID: memberID,
					Node: config.NodeConfig{
						NodeID:             nodeID,
						Address:            address,
						CoordinatorAddress: coordinatorAddress,
						StorageCapacity:    capacity,
						DataDir:            dataDir,
					},
				}
			}

			if cfg.MemberID == "" {
				return fmt.Errorf("member ID is required")
			}
			if cfg.Node.NodeID == "" {
				// Auto-generate node ID
				cfg.Node.NodeID = fmt.Sprintf("%s-node-%d", cfg.MemberID, os.Getpid())
			}

			// Create and start node
			var storageNode *node.Node
			if cfg.Auth != nil && cfg.Auth.Enabled {
				logger.Info("Starting node with authentication enabled")
				storageNode = node.NewWithAuth(&cfg.Node, cfg.MemberID, logger, cfg.Auth)
			} else {
				storageNode = node.New(&cfg.Node, cfg.MemberID, logger)
			}

			// Handle shutdown gracefully
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

			go func() {
				<-sigChan
				logger.Info("Shutting down storage node")
				storageNode.Stop()
				os.Exit(0)
			}()

			logger.Info("Starting storage node",
				zap.String("node_id", cfg.Node.NodeID),
				zap.String("member_id", cfg.MemberID),
				zap.String("address", cfg.Node.Address),
				zap.String("coordinator", cfg.Node.CoordinatorAddress),
				zap.String("capacity", utils.FormatDataSize(cfg.Node.StorageCapacity)))

			return storageNode.Start()
		},
	}

	cmd.Flags().StringVar(&memberID, "member-id", "", "member identifier this node belongs to")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "unique node identifier (auto-generated if not specified)")
	cmd.Flags().StringVar(&address, "address", ":7001", "node listening address")
	cmd.Flags().StringVar(&coordinatorAddress, "coordinator", "localhost:8001", "coordinator address to connect to")
	cmd.Flags().StringVar(&capacityStr, "capacity", "1GB", "storage capacity (e.g., 1GB, 500MB, 1.5TB)")
	cmd.Flags().StringVar(&dataDir, "data-dir", "./data", "directory for storing chunks")

	return cmd
}

func clientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Client operations for testing",
	}

	cmd.AddCommand(
		storeCmd(),
		retrieveCmd(),
		statusCmd(),
		mkdirCmd(),
		lsCmd(),
		rmCmd(),
		mvCmd(),
	)

	return cmd
}

func storeCmd() *cobra.Command {
	var (
		coordinatorAddr string
		filePath        string
		fileID          string
	)

	cmd := &cobra.Command{
		Use:   "store",
		Short: "Store a file in the collective",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(verbose)
			defer logger.Sync()

			// Get file info
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				return fmt.Errorf("failed to stat file: %w", err)
			}

			// Connect to coordinator
			conn, err := grpc.Dial(coordinatorAddr, grpc.WithInsecure())
			if err != nil {
				return fmt.Errorf("failed to connect to coordinator: %w", err)
			}
			defer conn.Close()

			client := protocol.NewCoordinatorClient(conn)
			ctx := context.Background()

			// Generate file ID if not provided
			if fileID == "" {
				fileID = fmt.Sprintf("/stored/%s", filepath.Base(filePath))
			}

			// For files larger than 4MB, use streaming
			const maxDirectSize = 4 * 1024 * 1024
			if fileInfo.Size() > maxDirectSize {
				// Create the file first
				createResp, err := client.CreateFile(ctx, &protocol.CreateFileRequest{
					Path: fileID,
					Mode: uint32(fileInfo.Mode()),
				})
				if err != nil {
					return fmt.Errorf("failed to create file: %w", err)
				}
				if !createResp.Success {
					return fmt.Errorf("failed to create file: %s", createResp.Message)
				}

				// Use streaming upload
				stream, err := client.WriteFileStream(ctx)
				if err != nil {
					return fmt.Errorf("failed to create stream: %w", err)
				}

				// Send header
				header := &protocol.WriteFileStreamRequest{
					Data: &protocol.WriteFileStreamRequest_Header{
						Header: &protocol.WriteFileStreamHeader{
							Path:      fileID,
							TotalSize: fileInfo.Size(),
						},
					},
				}
				if err := stream.Send(header); err != nil {
					return fmt.Errorf("failed to send header: %w", err)
				}

				// Open file for streaming
				file, err := os.Open(filePath)
				if err != nil {
					return fmt.Errorf("failed to open file: %w", err)
				}
				defer file.Close()

				// Stream file in chunks
				const chunkSize = 1024 * 1024 // 1MB chunks
				buffer := make([]byte, chunkSize)
				bytesSent := int64(0)
				
				for {
					n, err := file.Read(buffer)
					if err == io.EOF {
						break
					}
					if err != nil {
						return fmt.Errorf("failed to read file: %w", err)
					}

					chunk := &protocol.WriteFileStreamRequest{
						Data: &protocol.WriteFileStreamRequest_ChunkData{
							ChunkData: buffer[:n],
						},
					}
					
					if err := stream.Send(chunk); err != nil {
						return fmt.Errorf("failed to send chunk: %w", err)
					}
					
					bytesSent += int64(n)
					if bytesSent % (10 * 1024 * 1024) == 0 {
						logger.Info("Upload progress", 
							zap.Int64("sent", bytesSent),
							zap.Int64("total", fileInfo.Size()),
							zap.Float64("percent", float64(bytesSent)/float64(fileInfo.Size())*100))
					}
				}

				// Close stream and get response
				resp, err := stream.CloseAndRecv()
				if err != nil {
					return fmt.Errorf("failed to complete stream: %w", err)
				}

				if resp.Success {
					logger.Info("File stored successfully via streaming",
						zap.String("file_id", fileID),
						zap.Int64("bytes", resp.BytesWritten),
						zap.Int32("chunks", resp.ChunksCreated))
				} else {
					return fmt.Errorf("streaming upload failed: %s", resp.Message)
				}
			} else {
				// Small file - use direct upload
				data, err := os.ReadFile(filePath)
				if err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}

				// Create file
				createResp, err := client.CreateFile(ctx, &protocol.CreateFileRequest{
					Path: fileID,
					Mode: uint32(fileInfo.Mode()),
				})
				if err != nil {
					return fmt.Errorf("failed to create file: %w", err)
				}
				if !createResp.Success {
					return fmt.Errorf("failed to create file: %s", createResp.Message)
				}

				// Write file
				writeResp, err := client.WriteFile(ctx, &protocol.WriteFileRequest{
					Path: fileID,
					Data: data,
					Offset: 0,
				})
				if err != nil {
					return fmt.Errorf("failed to write file: %w", err)
				}

				if writeResp.Success {
					logger.Info("File stored successfully",
						zap.String("file_id", fileID),
						zap.Int64("bytes", writeResp.BytesWritten))
				} else {
					return fmt.Errorf("write failed: %s", writeResp.Message)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&coordinatorAddr, "coordinator", "localhost:8001", "coordinator address")
	cmd.Flags().StringVar(&filePath, "file", "", "path to file to store")
	cmd.Flags().StringVar(&fileID, "id", "", "file ID (auto-generated if not provided)")
	cmd.MarkFlagRequired("file")

	return cmd
}

func retrieveCmd() *cobra.Command {
	var (
		coordinatorAddr string
		fileID          string
		outputPath      string
	)

	cmd := &cobra.Command{
		Use:   "retrieve",
		Short: "Retrieve a file from the collective",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(verbose)
			defer logger.Sync()

			// Connect to coordinator
			conn, err := grpc.Dial(coordinatorAddr, grpc.WithInsecure())
			if err != nil {
				return fmt.Errorf("failed to connect to coordinator: %w", err)
			}
			defer conn.Close()

			client := protocol.NewCoordinatorClient(conn)
			ctx := context.Background()

			// Try streaming first for large files
			stream, err := client.ReadFileStream(ctx, &protocol.ReadFileStreamRequest{
				Path: fileID,
				Offset: 0,
				Length: 0, // Get entire file
			})
			
			if err != nil {
				// Fallback to non-streaming for small files
				resp, err := client.ReadFile(ctx, &protocol.ReadFileRequest{
					Path: fileID,
					Offset: 0,
					Length: 0,
				})
				
				if err != nil {
					return fmt.Errorf("failed to retrieve file: %w", err)
				}
				
				if !resp.Success {
					return fmt.Errorf("file not found")
				}
				
				// Write to output file
				if err := os.WriteFile(outputPath, resp.Data, 0644); err != nil {
					return fmt.Errorf("failed to write output file: %w", err)
				}
				
				logger.Info("File retrieved successfully",
					zap.String("file_id", fileID),
					zap.String("output", outputPath),
					zap.Int64("size", resp.BytesRead))
				
				return nil
			}
			
			// Handle streaming response
			outputFile, err := os.Create(outputPath)
			if err != nil {
				return fmt.Errorf("failed to create output file: %w", err)
			}
			defer outputFile.Close()
			
			var totalBytes int64
			var totalSize int64
			headerReceived := false
			
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					return fmt.Errorf("failed to receive stream: %w", err)
				}
				
				switch data := resp.Data.(type) {
				case *protocol.ReadFileStreamResponse_Header:
					headerReceived = true
					totalSize = data.Header.TotalSize
					logger.Info("Starting streaming download",
						zap.Int64("total_size", totalSize),
						zap.Int32("chunk_count", data.Header.ChunkCount))
						
				case *protocol.ReadFileStreamResponse_ChunkData:
					if !headerReceived {
						return fmt.Errorf("received data before header")
					}
					n, err := outputFile.Write(data.ChunkData)
					if err != nil {
						return fmt.Errorf("failed to write chunk: %w", err)
					}
					totalBytes += int64(n)
					
					// Progress reporting
					if totalSize > 0 && totalBytes % (10 * 1024 * 1024) == 0 {
						logger.Info("Download progress",
							zap.Int64("received", totalBytes),
							zap.Int64("total", totalSize),
							zap.Float64("percent", float64(totalBytes)/float64(totalSize)*100))
					}
				}
			}
			
			logger.Info("File retrieved successfully via streaming",
				zap.String("file_id", fileID),
				zap.String("output", outputPath),
				zap.Int64("size", totalBytes))

			return nil
		},
	}

	cmd.Flags().StringVar(&coordinatorAddr, "coordinator", "localhost:8001", "coordinator address")
	cmd.Flags().StringVar(&fileID, "id", "", "file ID to retrieve")
	cmd.Flags().StringVar(&outputPath, "output", "", "output file path")
	cmd.MarkFlagRequired("id")

	return cmd
}

func statusCmd() *cobra.Command {
	var (
		coordinatorAddr string
		jsonOutput     bool
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check collective status",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(false) // Quiet logging for clean output
			defer logger.Sync()

			// Define styles
			var (
				// Color palette
				primaryColor   = lipgloss.Color("#7571f9")
				warningColor   = lipgloss.Color("#ff9f43")
				dangerColor    = lipgloss.Color("#ff6b6b")
				mutedColor     = lipgloss.Color("#6c757d")
				
				// Muted text style
				mutedStyle = lipgloss.NewStyle().Foreground(mutedColor)
				
				// Base styles
				titleStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(primaryColor).
					MarginBottom(1)
				
				sectionStyle = lipgloss.NewStyle().
					Border(lipgloss.RoundedBorder()).
					BorderForeground(primaryColor).
					Padding(1).
					MarginBottom(1)
				
				headerStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(primaryColor).
					Underline(true).
					MarginBottom(1)
				
				warningStyle = lipgloss.NewStyle().
					Foreground(warningColor).
					Bold(true)
				
				dangerStyle = lipgloss.NewStyle().
					Foreground(dangerColor).
					Bold(true)
			)


			// Connect to coordinator
			conn, err := grpc.Dial(coordinatorAddr, grpc.WithInsecure())
			if err != nil {
				if jsonOutput {
					errorStatus := map[string]interface{}{
						"error":     true,
						"message":   "Connection Failed",
						"details":   err.Error(),
						"coordinator": coordinatorAddr,
						"timestamp": time.Now().Format(time.RFC3339),
					}
					jsonBytes, _ := json.MarshalIndent(errorStatus, "", "  ")
					fmt.Println(string(jsonBytes))
					return nil // Don't return error to avoid double output
				}
				
				errorBox := dangerStyle.Render("❌ Connection Failed") + "\n" +
					mutedStyle.Render(fmt.Sprintf("Cannot connect to %s", coordinatorAddr))
				fmt.Println(errorBox)
				return err
			}
			defer conn.Close()

			client := protocol.NewCoordinatorClient(conn)
			ctx := context.Background()

			// Send heartbeat to check status
			hbResp, err := client.Heartbeat(ctx, &protocol.HeartbeatRequest{
				MemberId:  "status-client",
				Timestamp: time.Now().Unix(),
			})

			if err != nil {
				if jsonOutput {
					errorStatus := map[string]interface{}{
						"error":     true,
						"message":   "Coordinator Unreachable", 
						"details":   err.Error(),
						"coordinator": coordinatorAddr,
						"timestamp": time.Now().Format(time.RFC3339),
					}
					jsonBytes, _ := json.MarshalIndent(errorStatus, "", "  ")
					fmt.Println(string(jsonBytes))
					return nil
				}
				
				errorBox := dangerStyle.Render("❌ Coordinator Unreachable") + "\n" +
					mutedStyle.Render(err.Error())
				fmt.Println(errorBox)
				return err
			}

			// Build title (skip if JSON output)
			if !jsonOutput {
				title := titleStyle.Render("🌐 COLLECTIVE STATUS")
				fmt.Println(title)
			}

			// Get detailed status
			statusResp, err := client.GetStatus(ctx, &protocol.GetStatusRequest{})
			if err != nil {
				if jsonOutput {
					errorStatus := map[string]interface{}{
						"error":     true,
						"message":   "Could not get detailed status",
						"details":   err.Error(),
						"timestamp": time.Now().Format(time.RFC3339),
					}
					jsonBytes, _ := json.MarshalIndent(errorStatus, "", "  ")
					fmt.Println(string(jsonBytes))
					return nil
				}
				
				errorBox := warningStyle.Render("⚠️  Could not get detailed status") + "\n" +
					mutedStyle.Render(err.Error())
				fmt.Println(errorBox)
				return nil
			}

			// Handle JSON output
			if jsonOutput {
				return outputStatusAsJSON(statusResp, hbResp, coordinatorAddr)
			}

			// Network info panel
			networkInfo := getNetworkInfo(coordinatorAddr)
			networkPanel := sectionStyle.Render(
				headerStyle.Render("🌐 NETWORK INFORMATION") + "\n\n" +
				networkInfo,
			)
			fmt.Println(networkPanel)

			// Create coordinators table
			coordTable := createCoordinatorsTable(statusResp, hbResp, coordinatorAddr)
			fmt.Println(coordTable)

			// Create nodes table
			allNodes := append([]*protocol.NodeInfo{}, statusResp.LocalNodes...)
			allNodes = append(allNodes, statusResp.RemoteNodes...)
			
			if len(allNodes) > 0 {
				nodesTable := createNodesTable(allNodes, statusResp.MemberId)
				fmt.Println(nodesTable)
			} else {
				noNodesBox := warningStyle.Render("⚠️  No nodes registered in the collective")
				fmt.Println(noNodesBox)
			}

			// Show data tree panel
			dataTreePanel := createDataTreePanel(client, ctx)
			fmt.Println(dataTreePanel)
				
			// Show collective summary table
			summaryTable := createSummaryTable(statusResp)
			fmt.Println(summaryTable)

			// Footer
			footer := lipgloss.NewStyle().
				Foreground(mutedColor).
				MarginTop(1).
				Render(fmt.Sprintf("Generated at %s", time.Now().Format("2006-01-02 15:04:05")))
			fmt.Println(footer)

			return nil
		},
	}

	cmd.Flags().StringVar(&coordinatorAddr, "coordinator", "localhost:8001", "coordinator address")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output status as JSON")

	return cmd
}

func peerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "peer",
		Short: "Peer management commands",
	}

	cmd.AddCommand(connectPeerCmd())
	return cmd
}

func connectPeerCmd() *cobra.Command {
	var (
		coordinatorAddr string
		peerID         string
		peerAddr       string
	)

	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect to a peer coordinator",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := grpc.Dial(coordinatorAddr, grpc.WithInsecure())
			if err != nil {
				return fmt.Errorf("failed to connect to coordinator: %w", err)
			}
			defer conn.Close()

			client := protocol.NewCoordinatorClient(conn)
			ctx := context.Background()

			resp, err := client.PeerConnect(ctx, &protocol.PeerConnectRequest{
				MemberId: peerID,
				Address:  peerAddr,
			})

			if err != nil {
				return fmt.Errorf("peer connection failed: %w", err)
			}

			if resp.Accepted {
				fmt.Printf("✓ Successfully connected to peer %s at %s\n", peerID, peerAddr)
			} else {
				fmt.Printf("✗ Peer connection rejected by %s\n", peerID)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&coordinatorAddr, "coordinator", "localhost:8001", "coordinator address")
	cmd.Flags().StringVar(&peerID, "peer-id", "", "peer member ID")
	cmd.Flags().StringVar(&peerAddr, "peer-addr", "", "peer address")
	cmd.MarkFlagRequired("peer-id")
	cmd.MarkFlagRequired("peer-addr")

	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Collective Storage System v0.1.0")
		},
	}
}

func setupLogger(verbose bool) *zap.Logger {
	config := zap.NewProductionConfig()
	if verbose {
		config.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	} else {
		config.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	}
	
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	
	logger, _ := config.Build()
	return logger
}

// Helper functions for lipgloss status display
func renderProgressBar(percent float64, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	
	filled := int(float64(width) * percent / 100)
	empty := width - filled
	
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("#42c767")).Render(strings.Repeat("█", filled))
	bar += lipgloss.NewStyle().Foreground(lipgloss.Color("#333333")).Render(strings.Repeat("░", empty))
	
	return fmt.Sprintf("%s %.1f%%", bar, percent)
}

func renderMiniBar(percent float64, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	
	filled := int(float64(width) * percent / 100)
	empty := width - filled
	
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#42c767")).Render(strings.Repeat("▪", filled)) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#333333")).Render(strings.Repeat("·", empty))
}

func getNetworkInfo(coordinatorAddr string) string {
	var lines []string
	
	// Get hostname
	hostname, _ := os.Hostname()
	lines = append(lines, fmt.Sprintf("Hostname: %s", hostname))
	
	// Parse coordinator address
	host, port, err := net.SplitHostPort(coordinatorAddr)
	if err == nil {
		lines = append(lines, fmt.Sprintf("Coordinator: %s:%s", host, port))
	} else {
		lines = append(lines, fmt.Sprintf("Coordinator: %s", coordinatorAddr))
	}
	
	// Get local IPs
	lines = append(lines, "\nLocal IP Addresses:")
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					lines = append(lines, fmt.Sprintf("  • %s", ipnet.IP.String()))
				}
			}
		}
	} else {
		lines = append(lines, "  (unable to get IPs)")
	}
	
	return strings.Join(lines, "\n")
}

func createCoordinatorsTable(statusResp *protocol.GetStatusResponse, hbResp *protocol.HeartbeatResponse, coordinatorAddr string) string {
	// Create table for coordinators
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("#7571f9"))).
		StyleFunc(func(row, col int) lipgloss.Style {
			switch {
			case row == 0:
				return lipgloss.NewStyle().
					Foreground(lipgloss.Color("#ffffff")).
					Bold(true).
					Padding(0, 1)
			default:
				return lipgloss.NewStyle().
					Padding(0, 1)
			}
		}).
		Headers("MEMBER ID", "ADDRESS", "STATUS", "CONNECTION", "NODES", "CAPACITY", "USED", "LAST SEEN")
	
	// Add main coordinator with colored status
	onlineStatus := lipgloss.NewStyle().Foreground(lipgloss.Color("#42c767")).Bold(true).Render("🔵 ONLINE")
	connectionType := lipgloss.NewStyle().Foreground(lipgloss.Color("#7571f9")).Bold(true).Render("LOCAL")
	t.Row(
		statusResp.MemberId,
		coordinatorAddr,
		onlineStatus,
		connectionType,
		fmt.Sprintf("%d", len(statusResp.LocalNodes)),
		formatBytes(statusResp.TotalStorageCapacity),
		formatBytes(statusResp.UsedStorageCapacity),
		"now",
	)
	
	// Add peers
	for _, peer := range statusResp.Peers {
		var status string
		if peer.IsHealthy {
			status = lipgloss.NewStyle().Foreground(lipgloss.Color("#42c767")).Render("🟢 CONNECTED")
		} else {
			status = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6b6b")).Render("🔴 UNHEALTHY")
		}
		
		lastSeen := "never"
		if peer.LastSeen > 0 {
			lastSeen = fmt.Sprintf("%s ago", time.Since(time.Unix(peer.LastSeen, 0)).Round(time.Second))
		}
		
		// Count nodes for this peer
		nodeCount := 0
		totalCap := int64(0)
		usedCap := int64(0)
		for _, node := range statusResp.RemoteNodes {
			if node.MemberId == peer.MemberId {
				nodeCount++
				totalCap += node.TotalCapacity
				usedCap += node.UsedCapacity
			}
		}
		
		// All peers in current architecture are direct connections
		connType := lipgloss.NewStyle().Foreground(lipgloss.Color("#42c767")).Render("DIRECT")
		
		t.Row(
			peer.MemberId,
			peer.Address,
			status,
			connType,
			fmt.Sprintf("%d", nodeCount),
			formatBytes(totalCap),
			formatBytes(usedCap),
			lastSeen,
		)
	}
	
	return lipgloss.NewStyle().
		MarginBottom(1).
		Render("📡 COORDINATORS\n" + t.Render())
}

func createNodesTable(nodes []*protocol.NodeInfo, localMemberID string) string {
	// Sort nodes for consistent display
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].MemberId == nodes[j].MemberId {
			return nodes[i].NodeId < nodes[j].NodeId
		}
		return nodes[i].MemberId < nodes[j].MemberId
	})
	
	// Create table for nodes
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("#00d2d3"))).
		StyleFunc(func(row, col int) lipgloss.Style {
			switch {
			case row == 0:
				return lipgloss.NewStyle().
					Foreground(lipgloss.Color("#ffffff")).
					Bold(true).
					Padding(0, 1)
			default:
				return lipgloss.NewStyle().
					Padding(0, 1)
			}
		}).
		Headers("NODE ID", "MEMBER", "ADDRESS", "STATUS", "CAPACITY", "USED", "USAGE", "TYPE")
	
	for _, node := range nodes {
		var status string
		if node.IsHealthy {
			status = lipgloss.NewStyle().Foreground(lipgloss.Color("#42c767")).Render("🟢 HEALTHY")
		} else {
			status = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6b6b")).Render("🔴 UNHEALTHY")
		}
		
		usage := float64(0)
		if node.TotalCapacity > 0 {
			usage = float64(node.UsedCapacity) / float64(node.TotalCapacity) * 100
		}
		
		var nodeType string
		if node.MemberId == localMemberID {
			nodeType = lipgloss.NewStyle().Foreground(lipgloss.Color("#00d2d3")).Bold(true).Render("🏠 LOCAL")
		} else {
			nodeType = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff9f43")).Render("🌍 REMOTE")
		}
		
		// Color usage percentage based on value
		usageColor := lipgloss.Color("#42c767") // Green
		if usage > 80 {
			usageColor = lipgloss.Color("#ff6b6b") // Red
		} else if usage > 60 {
			usageColor = lipgloss.Color("#ff9f43") // Orange
		}
		
		usageBar := renderMiniBar(usage, 10)
		usageText := lipgloss.NewStyle().Foreground(usageColor).Render(fmt.Sprintf("%.1f%%", usage))
		
		t.Row(
			node.NodeId,
			node.MemberId,
			node.Address,
			status,
			formatBytes(node.TotalCapacity),
			formatBytes(node.UsedCapacity),
			fmt.Sprintf("%s %s", usageBar, usageText),
			nodeType,
		)
	}
	
	return lipgloss.NewStyle().
		MarginBottom(1).
		Render("💾 STORAGE NODES\n" + t.Render())
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func createSummaryTable(statusResp *protocol.GetStatusResponse) string {
	// Calculate totals
	var totalCapacity, totalUsed int64
	totalNodes := len(statusResp.LocalNodes) + len(statusResp.RemoteNodes)
	totalCoordinators := 1 + len(statusResp.Peers)
	
	// Add all capacity
	for _, node := range statusResp.LocalNodes {
		totalCapacity += node.TotalCapacity
		totalUsed += node.UsedCapacity
	}
	for _, node := range statusResp.RemoteNodes {
		totalCapacity += node.TotalCapacity
		totalUsed += node.UsedCapacity
	}
	
	usagePercent := float64(0)
	if totalCapacity > 0 {
		usagePercent = float64(totalUsed) / float64(totalCapacity) * 100
	}
	
	// Create summary table
	t := table.New().
		Border(lipgloss.DoubleBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("#7571f9")).Bold(true)).
		StyleFunc(func(row, col int) lipgloss.Style {
			switch {
			case row == 0:
				return lipgloss.NewStyle().
					Foreground(lipgloss.Color("#ffffff")).
					Bold(true).
					Padding(0, 1)
			default:
				return lipgloss.NewStyle().
					Padding(0, 1)
			}
		}).
		Headers("METRIC", "VALUE", "VISUALIZATION")
	
	// Color for usage
	usageColor := lipgloss.Color("#42c767") // Green
	if usagePercent > 80 {
		usageColor = lipgloss.Color("#ff6b6b") // Red
	} else if usagePercent > 60 {
		usageColor = lipgloss.Color("#ff9f43") // Orange
	}
	
	// Add rows
	t.Row(
		"🌐 Total Coordinators",
		fmt.Sprintf("%d", totalCoordinators),
		"",
	)
	t.Row(
		"💾 Total Nodes",
		fmt.Sprintf("%d", totalNodes),
		"",
	)
	t.Row(
		"📦 Total Capacity",
		formatBytes(totalCapacity),
		"",
	)
	t.Row(
		"📋 Used Storage",
		formatBytes(totalUsed),
		renderProgressBar(usagePercent, 30),
	)
	t.Row(
		"✅ Available Storage",
		formatBytes(totalCapacity - totalUsed),
		lipgloss.NewStyle().Foreground(usageColor).Render(fmt.Sprintf("%.1f%% used", usagePercent)),
	)
	
	return lipgloss.NewStyle().
		MarginBottom(1).
		Render("📊 COLLECTIVE SUMMARY\n" + t.Render())
}

func renderStatCard(label, value string, color lipgloss.Color) string {
	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6c757d")).
		Bold(false)
	
	valueStyle := lipgloss.NewStyle().
		Foreground(color).
		Bold(true)
	
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(color).
		Padding(0, 1).
		Width(18).
		Render(
			labelStyle.Render(label) + "\n" +
			valueStyle.Render(value),
		)
}

// Directory operation commands

func mkdirCmd() *cobra.Command {
	var (
		coordinatorAddr string
		mode           uint32
	)

	cmd := &cobra.Command{
		Use:   "mkdir [path]",
		Short: "Create a directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(verbose)
			defer logger.Sync()

			path := args[0]

			conn, err := grpc.Dial(coordinatorAddr, grpc.WithInsecure())
			if err != nil {
				return fmt.Errorf("failed to connect to coordinator: %w", err)
			}
			defer conn.Close()

			client := protocol.NewCoordinatorClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			resp, err := client.CreateDirectory(ctx, &protocol.CreateDirectoryRequest{
				Path: path,
				Mode: mode,
			})
			if err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}

			if resp.Success {
				fmt.Printf("Directory created: %s\n", path)
			} else {
				fmt.Printf("Failed to create directory: %s\n", resp.Message)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&coordinatorAddr, "coordinator", "localhost:8001", "coordinator address")
	cmd.Flags().Uint32Var(&mode, "mode", 0755, "directory mode (permissions)")
	return cmd
}

func lsCmd() *cobra.Command {
	var coordinatorAddr string

	cmd := &cobra.Command{
		Use:   "ls [path]",
		Short: "List directory contents",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(verbose)
			defer logger.Sync()

			path := "/"
			if len(args) > 0 {
				path = args[0]
			}

			conn, err := grpc.Dial(coordinatorAddr, grpc.WithInsecure())
			if err != nil {
				return fmt.Errorf("failed to connect to coordinator: %w", err)
			}
			defer conn.Close()

			client := protocol.NewCoordinatorClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			resp, err := client.ListDirectory(ctx, &protocol.ListDirectoryRequest{
				Path: path,
			})
			if err != nil {
				return fmt.Errorf("failed to list directory: %w", err)
			}

			if !resp.Success {
				return fmt.Errorf("directory not found: %s", path)
			}

			// Print directory listing
			fmt.Printf("Contents of %s:\n", path)
			for _, entry := range resp.Entries {
				typeChar := "-"
				if entry.IsDirectory {
					typeChar = "d"
				}
				
				modTime := time.Unix(entry.ModifiedTime, 0).Format("2006-01-02 15:04")
				fmt.Printf("%s %o %8s %s %s\n", 
					typeChar,
					entry.Mode,
					formatBytes(entry.Size),
					modTime,
					entry.Name)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&coordinatorAddr, "coordinator", "localhost:8001", "coordinator address")
	return cmd
}

func rmCmd() *cobra.Command {
	var (
		coordinatorAddr string
		recursive       bool
	)

	cmd := &cobra.Command{
		Use:   "rm [path]",
		Short: "Remove a directory or file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(verbose)
			defer logger.Sync()

			path := args[0]

			conn, err := grpc.Dial(coordinatorAddr, grpc.WithInsecure())
			if err != nil {
				return fmt.Errorf("failed to connect to coordinator: %w", err)
			}
			defer conn.Close()

			client := protocol.NewCoordinatorClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// First check if it's a directory or file
			statResp, err := client.StatEntry(ctx, &protocol.StatEntryRequest{
				Path: path,
			})
			if err != nil {
				return fmt.Errorf("failed to stat entry: %w", err)
			}
			if !statResp.Success {
				return fmt.Errorf("path not found: %s", path)
			}

			if statResp.Entry.IsDirectory {
				// Remove directory
				resp, err := client.DeleteDirectory(ctx, &protocol.DeleteDirectoryRequest{
					Path:      path,
					Recursive: recursive,
				})
				if err != nil {
					return fmt.Errorf("failed to delete directory: %w", err)
				}

				if resp.Success {
					fmt.Printf("Directory removed: %s\n", path)
				} else {
					fmt.Printf("Failed to remove directory: %s\n", resp.Message)
				}
			} else {
				// TODO: Implement file deletion once we have file entries working
				return fmt.Errorf("file deletion not yet implemented")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&coordinatorAddr, "coordinator", "localhost:8001", "coordinator address")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "remove directories recursively")
	return cmd
}

func mvCmd() *cobra.Command {
	var coordinatorAddr string

	cmd := &cobra.Command{
		Use:   "mv [source] [destination]",
		Short: "Move/rename a directory or file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(verbose)
			defer logger.Sync()

			oldPath := args[0]
			newPath := args[1]

			conn, err := grpc.Dial(coordinatorAddr, grpc.WithInsecure())
			if err != nil {
				return fmt.Errorf("failed to connect to coordinator: %w", err)
			}
			defer conn.Close()

			client := protocol.NewCoordinatorClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			resp, err := client.MoveEntry(ctx, &protocol.MoveEntryRequest{
				OldPath: oldPath,
				NewPath: newPath,
			})
			if err != nil {
				return fmt.Errorf("failed to move entry: %w", err)
			}

			if resp.Success {
				fmt.Printf("Moved: %s -> %s\n", oldPath, newPath)
			} else {
				fmt.Printf("Failed to move entry: %s\n", resp.Message)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&coordinatorAddr, "coordinator", "localhost:8001", "coordinator address")
	return cmd
}
func mountCmd() *cobra.Command {
	var (
		coordinatorAddr string
	)

	cmd := &cobra.Command{
		Use:   "mount [mountpoint]",
		Short: "Mount the collective storage as a filesystem",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger(verbose)
			defer logger.Sync()

			mountpoint := args[0]
			
			logger.Info("Mounting collective storage", 
				zap.String("mountpoint", mountpoint),
				zap.String("coordinator", coordinatorAddr))

			return mountCollectiveFS(coordinatorAddr, mountpoint, logger)
		},
	}

	cmd.Flags().StringVar(&coordinatorAddr, "coordinator", "localhost:8001", "coordinator address")
	return cmd
}

func mountCollectiveFS(coordinatorAddr, mountpoint string, logger *zap.Logger) error {
	// Create and mount the FUSE filesystem
	return collectivefuse.Mount(coordinatorAddr, mountpoint, logger)
}

// outputStatusAsJSON outputs a comprehensive JSON status of the entire collective
func outputStatusAsJSON(statusResp *protocol.GetStatusResponse, hbResp *protocol.HeartbeatResponse, coordinatorAddr string) error {
	// Build comprehensive status structure
	status := map[string]interface{}{
		"timestamp":       time.Now().Format(time.RFC3339),
		"collective_info": map[string]interface{}{
			"queried_coordinator": coordinatorAddr,
			"response_time_ms":    calculateResponseTime(),
			"cluster_health":      "healthy", // TODO: calculate based on node/coordinator states
		},
		"coordinator_info": map[string]interface{}{
			"member_id":           statusResp.MemberId,
			"address":            coordinatorAddr,
			"heartbeat_response": map[string]interface{}{
				"member_id":  hbResp.MemberId,
				"timestamp":  hbResp.Timestamp,
				"responded":  true,
			},
		},
		"peers": buildPeersInfo(statusResp.Peers),
		"nodes": map[string]interface{}{
			"local_nodes":  buildNodesInfo(statusResp.LocalNodes, statusResp.MemberId, true),
			"remote_nodes": buildNodesInfo(statusResp.RemoteNodes, statusResp.MemberId, false),
			"total_count":  len(statusResp.LocalNodes) + len(statusResp.RemoteNodes),
		},
		"capacity": map[string]interface{}{
			"total_capacity_bytes": statusResp.TotalStorageCapacity,
			"used_capacity_bytes":  statusResp.UsedStorageCapacity,
			"available_bytes":      statusResp.TotalStorageCapacity - statusResp.UsedStorageCapacity,
			"utilization_percent":  calculateUtilization(statusResp.TotalStorageCapacity, statusResp.UsedStorageCapacity),
		},
		"file_info": map[string]interface{}{
			"total_files": statusResp.TotalFiles,
		},
		"network": map[string]interface{}{
			"coordinator_connectivity": buildNetworkInfo(coordinatorAddr),
		},
		"debug_info": map[string]interface{}{
			"coordinator_version": "0.1.0",
			"protocol_version":    "1.0",
			"build_info": map[string]interface{}{
				"go_version": "1.21+",
				"platform":   "multi",
			},
		},
	}

	// Marshal and output JSON
	jsonBytes, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal status to JSON: %w", err)
	}

	fmt.Println(string(jsonBytes))
	return nil
}

// buildPeersInfo converts peer info to structured format
func buildPeersInfo(peers []*protocol.PeerInfo) []map[string]interface{} {
	var peersList []map[string]interface{}
	
	for _, peer := range peers {
		peerInfo := map[string]interface{}{
			"member_id":      peer.MemberId,
			"address":       peer.Address,
			"is_healthy":    peer.IsHealthy,
			"last_seen":     time.Unix(peer.LastSeen, 0).Format(time.RFC3339),
			"last_seen_ago": fmt.Sprintf("%ds", time.Now().Unix()-peer.LastSeen),
			"status":        getHealthStatus(peer.IsHealthy),
		}
		peersList = append(peersList, peerInfo)
	}
	
	return peersList
}

// buildNodesInfo converts node info to structured format
func buildNodesInfo(nodes []*protocol.NodeInfo, currentMember string, isLocal bool) []map[string]interface{} {
	var nodesList []map[string]interface{}
	
	for _, node := range nodes {
		utilizationPercent := float64(0)
		if node.TotalCapacity > 0 {
			utilizationPercent = float64(node.UsedCapacity) / float64(node.TotalCapacity) * 100
		}
		
		nodeInfo := map[string]interface{}{
			"node_id":             node.NodeId,
			"member_id":           node.MemberId,
			"address":            node.Address,
			"is_local":           isLocal,
			"is_healthy":         node.IsHealthy,
			"status":             getHealthStatus(node.IsHealthy),
			"capacity": map[string]interface{}{
				"total_bytes":        node.TotalCapacity,
				"used_bytes":         node.UsedCapacity,
				"available_bytes":    node.TotalCapacity - node.UsedCapacity,
				"utilization_percent": fmt.Sprintf("%.1f", utilizationPercent),
			},
		}
		nodesList = append(nodesList, nodeInfo)
	}
	
	return nodesList
}

// buildNetworkInfo provides network connectivity details
func buildNetworkInfo(coordinatorAddr string) map[string]interface{} {
	host, port, err := net.SplitHostPort(coordinatorAddr)
	if err != nil {
		host = coordinatorAddr
		port = "unknown"
	}
	
	return map[string]interface{}{
		"coordinator_host": host,
		"coordinator_port": port,
		"connection_type":  "grpc",
		"security":        "insecure", // Current state
		"reachable":       true,       // If we got here, it's reachable
	}
}

// Helper functions
func calculateResponseTime() int {
	// TODO: measure actual response time
	return 50 // placeholder
}

func calculateUtilization(total, used int64) string {
	if total == 0 {
		return "0.0"
	}
	utilization := float64(used) / float64(total) * 100
	return fmt.Sprintf("%.1f", utilization)
}

func getHealthStatus(isHealthy bool) string {
	if isHealthy {
		return "online"
	}
	return "offline"
}
