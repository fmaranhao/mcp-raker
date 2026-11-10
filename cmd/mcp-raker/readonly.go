package main

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/cockroachdb/errors"
	"github.com/lexfrei/mcp-raker/internal/moonraker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type diagnosticFileParams struct {
	Root      string `json:"root,omitempty"       jsonschema:"Moonraker root: config, gcodes or logs; default config"`
	Filename  string `json:"filename"             jsonschema:"Relative filename within the root, such as mmu/base/mmu_parameters.cfg or klippy.log"`
	Offset    int64  `json:"offset,omitempty"     jsonschema:"Byte offset for a forward read; not used with tail"`
	Limit     int64  `json:"limit,omitempty"      jsonschema:"Maximum bytes, 1 to 262144; default 65536"`
	Tail      bool   `json:"tail,omitempty"       jsonschema:"Read last limit bytes; defaults true for logs and false for other roots"`
	FromStart bool   `json:"from_start,omitempty" jsonschema:"Disable default log tail and read from offset instead"`
}

type diagnosticFileResult struct {
	Root     string `json:"root"`
	Filename string `json:"filename"`
	Bytes    int    `json:"bytes"`
	Tail     bool   `json:"tail"`
	Content  string `json:"content"`
}

func diagnosticFileTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "moonraker_files_download",
		Description: "Read a bounded file slice/tail from config, gcodes or logs in printer_data via Moonraker. Logs default to last 64 KiB. Never uploads, modifies or deletes files. Content may contain credentials; do not publish it.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}
}

type fileReader interface {
	ReadFile(ctx context.Context, root, filename string, offset, limit int64, tail bool) ([]byte, error)
}

func diagnosticFileHandler(api moonraker.API) mcp.ToolHandlerFor[diagnosticFileParams, diagnosticFileResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, params diagnosticFileParams) (*mcp.CallToolResult, diagnosticFileResult, error) {
		var out diagnosticFileResult

		reader, ok := api.(fileReader)
		if !ok {
			return nil, out, errors.New("bounded file reader unavailable")
		}

		if params.Root == "" {
			params.Root = "config"
		}

		if params.Limit == 0 {
			params.Limit = 65536
		}

		if params.Tail && params.FromStart {
			return nil, out, errors.New("tail and from_start are mutually exclusive")
		}

		tail := params.Tail || (params.Root == "logs" && !params.FromStart)

		data, err := reader.ReadFile(ctx, params.Root, params.Filename, params.Offset, params.Limit, tail)
		if err != nil {
			return nil, out, errors.Wrap(err, "read diagnostic file")
		}

		bytesRead := len(data)
		if !utf8.Valid(data) {
			data = []byte(strings.ToValidUTF8(string(data), "\uFFFD"))
		}

		out = diagnosticFileResult{Root: params.Root, Filename: params.Filename, Bytes: bytesRead, Tail: tail, Content: string(data)}

		return nil, out, nil
	}
}
