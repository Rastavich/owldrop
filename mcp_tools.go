package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

const mcpGetFileMax = 1 << 20
const mcpSendFileMax = 32 << 20

func mcpTooLarge(size int64) error {
	return fmt.Errorf("too_large: size %d; use save_file", size)
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
	default:
		return nil, fmt.Errorf("unknown tool")
	}
}
