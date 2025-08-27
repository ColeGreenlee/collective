package fuse

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Ensure CollectiveFS implements all required interfaces
var _ fs.NodeStatfser = (*CollectiveFS)(nil)
var _ fs.NodeGetattrer = (*CollectiveFS)(nil)
var _ fs.NodeReaddirer = (*CollectiveFS)(nil)
var _ fs.NodeLookuper = (*CollectiveFS)(nil)
var _ fs.NodeCreater = (*CollectiveFS)(nil)
var _ fs.NodeMkdirer = (*CollectiveFS)(nil)
var _ fs.NodeRmdirer = (*CollectiveFS)(nil)
var _ fs.NodeUnlinker = (*CollectiveFS)(nil)
var _ fs.NodeOpener = (*CollectiveFS)(nil)
var _ fs.NodeRenamer = (*CollectiveFS)(nil)
var _ fs.NodeSetattrer = (*CollectiveFS)(nil)
var _ fs.NodeAccesser = (*CollectiveFS)(nil)

// RootNode represents the root of the filesystem
type RootNode struct {
	*CollectiveFS
}

// NewRootNode creates the root node for the filesystem
func NewRootNode(cfs *CollectiveFS) *RootNode {
	return &RootNode{
		CollectiveFS: cfs,
	}
}

// Statfs for the root node - provides filesystem statistics
func (r *RootNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	return r.CollectiveFS.Statfs(ctx, out)
}
