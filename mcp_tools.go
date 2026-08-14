package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"tailscale.com/tailcfg"
)

const mcpGetFileMax = 1 << 20
const mcpSendFileMax = 32 << 20
const mcpSyncFileMax = 4 << 20
const mcpSyncTextRunes = 2048

func mcpTooLarge(size int64) error {
	return fmt.Errorf("too_large: size %d; use save_file", size)
}

func mcpSendDenyReason(reason string) string {
	if isTaggedTaildropBlock(reason) {
		return "tagged peers cannot receive Taildrop files"
	}
	if reason == "available" {
		return ""
	}
	if reason == "" {
		return "peer availability is unknown"
	}
	return "peer unavailable: " + reason
}

func mcpDecodeSendData(encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("data must be valid base64: %w", err)
	}
	if len(data) > mcpSendFileMax {
		return nil, fmt.Errorf("too_large: decoded payload size %d exceeds %d", len(data), mcpSendFileMax)
	}
	return data, nil
}

func mcpDecodeSyncData(encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("data must be valid base64: %w", err)
	}
	if len(data) > mcpSyncFileMax {
		return nil, fmt.Errorf("too_large: decoded payload size %d exceeds %d", len(data), mcpSyncFileMax)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty file")
	}
	return data, nil
}

func mcpStringArg(args map[string]any, name string, required bool) (string, error) {
	value, ok := args[name]
	if !ok {
		if required {
			return "", fmt.Errorf("%s required", name)
		}
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	if required && text == "" {
		return "", fmt.Errorf("%s required", name)
	}
	return text, nil
}

func mcpFileArgs(args map[string]any, withDir bool) (name, dir, source string, err error) {
	if name, err = mcpStringArg(args, "name", true); err != nil {
		return "", "", "", err
	}
	if !validBaseName(name) {
		return "", "", "", fmt.Errorf("bad file name")
	}
	if withDir {
		if dir, err = mcpStringArg(args, "dir", false); err != nil {
			return "", "", "", err
		}
	}
	if source, err = mcpStringArg(args, "source", false); err != nil {
		return "", "", "", err
	}
	return name, dir, source, nil
}

func mcpPeerArgs(args map[string]any) ([]tailcfg.StableNodeID, error) {
	peer, err := mcpStringArg(args, "peer", true)
	if err != nil {
		return nil, err
	}
	peers := []tailcfg.StableNodeID{tailcfg.StableNodeID(peer)}
	extra, ok := args["peers"]
	if !ok {
		return peers, nil
	}
	var values []string
	switch extra := extra.(type) {
	case []string:
		values = extra
	case []any:
		values = make([]string, 0, len(extra))
		for _, value := range extra {
			peer, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("peers must be an array of strings")
			}
			values = append(values, peer)
		}
	default:
		return nil, fmt.Errorf("peers must be an array of strings")
	}
	for _, peer := range values {
		if peer == "" {
			return nil, fmt.Errorf("peers must be an array of strings")
		}
		peers = append(peers, tailcfg.StableNodeID(peer))
	}
	return peers, nil
}

func (s *server) mcpGetFile(ctx context.Context, name, source string) (map[string]any, error) {
	files, err := s.combinedInbox(ctx)
	if err != nil {
		return nil, err
	}
	var waiting *waitingFile
	for i := range files {
		if files[i].Name == name && files[i].Source == source {
			waiting = &files[i]
			break
		}
	}
	if waiting == nil {
		return nil, fmt.Errorf("no such file")
	}
	if waiting.Size > mcpGetFileMax {
		return nil, mcpTooLarge(waiting.Size)
	}

	var (
		reader io.ReadCloser
		size   int64
	)
	if source == "link" {
		file := s.drops.file(name)
		if file == nil {
			return nil, fmt.Errorf("no such file")
		}
		size = file.Size
		reader, err = os.Open(file.Path)
	} else {
		reader, size, err = tsClient.GetWaitingFile(ctx, name)
		if size > mcpGetFileMax {
			if reader != nil {
				reader.Close()
			}
			return nil, mcpTooLarge(size)
		}
	}
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	data, err := io.ReadAll(io.LimitReader(reader, mcpGetFileMax+1))
	if err != nil {
		return nil, err
	}
	if len(data) > mcpGetFileMax {
		return nil, mcpTooLarge(int64(len(data)))
	}
	return map[string]any{
		"name": name,
		"size": size,
		"data": base64.StdEncoding.EncodeToString(data),
	}, nil
}

func (s *server) mcpCallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "list_devices":
		devices, err := s.tsDevicesVisible(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(devices))
		for _, d := range devices {
			out = append(out, map[string]any{
				"id":       d.ID,
				"name":     d.Name,
				"os":       d.OS,
				"online":   d.Online,
				"taildrop": d.Taildrop,
				"tagged":   isTaggedTaildropBlock(d.Taildrop),
			})
		}
		return map[string]any{"devices": out}, nil
	case "send_file":
		peers, err := mcpPeerArgs(args)
		if err != nil {
			return nil, err
		}
		fileName, err := mcpStringArg(args, "name", true)
		if err != nil {
			return nil, err
		}
		if !validBaseName(fileName) {
			return nil, fmt.Errorf("bad file name")
		}
		encoded, ok := args["data"]
		if !ok {
			return nil, fmt.Errorf("data required")
		}
		encodedData, ok := encoded.(string)
		if !ok {
			return nil, fmt.Errorf("data must be a string")
		}
		data, err := mcpDecodeSendData(encodedData)
		if err != nil {
			return nil, err
		}

		devices, err := s.tsDevicesVisible(ctx)
		if err != nil {
			return nil, err
		}
		byID := make(map[tailcfg.StableNodeID]device, len(devices))
		for _, d := range devices {
			byID[d.ID] = d
		}
		for _, peer := range peers {
			d, ok := byID[peer]
			if !ok {
				return nil, fmt.Errorf("peer %q is not available", peer)
			}
			if reason := mcpSendDenyReason(d.Taildrop); reason != "" {
				return nil, fmt.Errorf("peer %q: %s", peer, reason)
			}
		}
		for _, peer := range peers {
			if err := s.sendOne(ctx, "mcp-"+newToken(), peer, fileName, int64(len(data)), bytes.NewReader(data), nil); err != nil {
				return nil, err
			}
		}
		return map[string]any{"ok": true}, nil
	case "list_inbox":
		files, err := s.combinedInbox(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"files": files}, nil
	case "save_file":
		fileName, dir, source, err := mcpFileArgs(args, true)
		if err != nil {
			return nil, err
		}
		if dir == "" {
			dir = s.saveDir()
		}
		var path string
		if source == "link" {
			path, err = s.linkSave(fileName, dir)
			if err == nil {
				s.broadcastInboxNow()
			}
		} else {
			path, err = s.saveOne(ctx, fileName, dir, nil)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": path}, nil
	case "delete_file":
		fileName, _, source, err := mcpFileArgs(args, false)
		if err != nil {
			return nil, err
		}
		if source == "link" {
			err = s.deleteLinkFile(fileName)
		} else {
			err = s.deleteInboxFile(ctx, fileName)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "get_file":
		fileName, _, source, err := mcpFileArgs(args, false)
		if err != nil {
			return nil, err
		}
		return s.mcpGetFile(ctx, fileName, source)
	case "list_sync":
		items := s.sync.list()
		for i := range items {
			runes := []rune(items[i].Text)
			if len(runes) > mcpSyncTextRunes {
				items[i].Text = string(runes[:mcpSyncTextRunes])
			}
		}
		return map[string]any{"items": items}, nil
	case "post_sync":
		_, hasName := args["name"]
		_, hasData := args["data"]
		if hasName || hasData {
			fileName, err := mcpStringArg(args, "name", true)
			if err != nil {
				return nil, err
			}
			if !validBaseName(fileName) {
				return nil, fmt.Errorf("bad file name")
			}
			encoded, err := mcpStringArg(args, "data", true)
			if err != nil {
				return nil, err
			}
			data, err := mcpDecodeSyncData(encoded)
			if err != nil {
				return nil, err
			}
			id := newSyncID()
			dir := filepath.Join(s.sync.dir, syncDirName)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("cannot store file: %w", err)
			}
			path := filepath.Join(dir, id+"-"+fileName)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return nil, fmt.Errorf("cannot store file: %w", err)
			}
			item := s.sync.addFile(id, fileName, int64(len(data)))
			tele.event("sync_item_added")
			s.syncChanged()
			return item, nil
		}
		text, err := mcpStringArg(args, "text", true)
		if err != nil {
			return nil, err
		}
		item, err := s.sync.addText(text)
		if err != nil {
			return nil, err
		}
		tele.event("sync_item_added")
		s.syncChanged()
		return item, nil
	case "create_drop_link":
		linkName, err := mcpStringArg(args, "name", true)
		if err != nil {
			return nil, err
		}
		ttlMinutes := 60.0
		if value, ok := args["ttl_minutes"]; ok {
			var valid bool
			ttlMinutes, valid = value.(float64)
			if !valid || math.IsNaN(ttlMinutes) || math.IsInf(ttlMinutes, 0) {
				return nil, fmt.Errorf("ttl_minutes must be a number")
			}
		}
		if ttlMinutes <= 0 {
			ttlMinutes = 60
		}
		if ttlMinutes > 7*24*60 {
			ttlMinutes = 7 * 24 * 60
		}
		maxUses := 0
		if value, ok := args["max_uses"]; ok {
			number, valid := value.(float64)
			if !valid ||
				math.IsNaN(number) || math.IsInf(number, 0) ||
				number < 0 ||
				number != math.Trunc(number) ||
				number >= 1<<63 {
				return nil, fmt.Errorf("max_uses must be a non-negative integer")
			}
			maxUses = int(number)
		}
		if value, ok := args["single"]; ok {
			single, valid := value.(bool)
			if !valid {
				return nil, fmt.Errorf("single must be a boolean")
			}
			if single {
				maxUses = 1
			}
		}
		link := s.drops.create(linkName, time.Duration(ttlMinutes*float64(time.Minute)), maxUses, 0)
		localURL := s.dropBaseURL() + "drop/" + link.Token
		publicURL := ""
		if s.funnelEnabled() {
			publicURL = s.funnelPublicURL() + "drop/" + link.Token
		}
		result := map[string]any{
			"link":      link,
			"local_url": localURL,
			"share_url": shareableDropURL(localURL, publicURL),
		}
		if publicURL != "" {
			result["public_url"] = publicURL
		}
		return result, nil
	case "list_drop_links":
		publicBaseURL := ""
		if s.funnelEnabled() {
			publicBaseURL = s.funnelPublicURL()
		}
		localBaseURL := s.dropBaseURL()
		now := time.Now()
		links := make([]map[string]any, 0)
		for _, link := range s.drops.list() {
			if link.Revoked || now.After(link.Expires) {
				continue
			}
			localURL := localBaseURL + "drop/" + link.Token
			publicURL := ""
			if publicBaseURL != "" {
				publicURL = publicBaseURL + "drop/" + link.Token
			}
			row := map[string]any{
				"link":      link,
				"local_url": localURL,
				"share_url": shareableDropURL(localURL, publicURL),
			}
			if publicURL != "" {
				row["public_url"] = publicURL
			}
			links = append(links, row)
		}
		return map[string]any{"links": links}, nil
	default:
		return nil, fmt.Errorf("unknown tool")
	}
}
