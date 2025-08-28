// Package federation implements the Collective Storage Federation system for media server collectives.
// It provides Mastodon-style addressing (node@member.domain), hierarchical certificate authority,
// gossip-based peer discovery, DataStore-level permissions, and smart chunk placement optimized 
// for both high-bandwidth media serving and reliable backups.
package federation