// `dlg mcp`: the Core operations as a Model Context Protocol server over stdio
// (newline-delimited JSON-RPC 2.0). Hand-rolled: it only needs initialize, ping,
// tools/list and tools/call.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

const mcpLatest = "2025-06-18"

var mcpVersions = []string{mcpLatest, "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"` // absent for notifications
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema D      `json:"inputSchema"`
	call        func(c Core, args json.RawMessage) (string, error)
}

func schema(required []string, props D) D {
	return D{"type": "object", "properties": props, "required": required}
}

var (
	pidProp  = D{"type": "integer", "description": "problem id (see dlg_problems)"}
	textProp = func(desc string) D { return D{"type": "string", "description": desc} }
)

func jsonText(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return string(b), err
}

var mcpTools = []mcpTool{
	{"dlg_problems", "List the problems in the decision ledger with their status.", schema([]string{}, D{}),
		func(c Core, _ json.RawMessage) (string, error) {
			v, err := c.Problems()
			if err != nil {
				return "", err
			}
			return jsonText(v)
		}},
	{"dlg_status", "Where a problem stands: round, board counts, reviewers waiting for a reply, replies not yet analysed.",
		schema([]string{"problem_id"}, D{"problem_id": pidProp}),
		func(c Core, raw json.RawMessage) (string, error) {
			var a struct {
				ProblemID int64 `json:"problem_id"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			v, err := c.Status(a.ProblemID)
			if err != nil {
				return "", err
			}
			return jsonText(v)
		}},
	{"dlg_handoff", "Get the text to review for a reviewer: full context first, then only the delta. Marks it as sent unless peek is true. Reply with dlg_review in the format the text asks for.",
		schema([]string{"problem_id", "to"}, D{"problem_id": pidProp, "to": textProp("reviewer name, e.g. gemini"),
			"peek": D{"type": "boolean", "description": "do not mark the handoff as sent"}}),
		func(c Core, raw json.RawMessage) (string, error) {
			var a struct {
				ProblemID int64  `json:"problem_id"`
				To        string `json:"to"`
				Peek      bool   `json:"peek"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			h, err := c.Handoff(a.ProblemID, a.To, !a.Peek)
			if err != nil {
				return "", err
			}
			return h.Text, nil
		}},
	{"dlg_review", "Store a reviewer's reply verbatim. Use the reply format from the handoff ([P1] AGREE|DISAGREE: why). Returns the review id and format warnings.",
		schema([]string{"problem_id", "author", "body"}, D{"problem_id": pidProp, "author": textProp("reviewer name"),
			"body": textProp("the full reply")}),
		func(c Core, raw json.RawMessage) (string, error) {
			var a struct {
				ProblemID int64  `json:"problem_id"`
				Author    string `json:"author"`
				Body      string `json:"body"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			v, err := c.Review(a.ProblemID, a.Author, a.Body, nil)
			if err != nil {
				return "", err
			}
			return jsonText(v)
		}},
	{"dlg_note", "Add a note (context or an answer) to a problem.",
		schema([]string{"problem_id", "text"}, D{"problem_id": pidProp, "text": textProp("the note")}),
		func(c Core, raw json.RawMessage) (string, error) {
			var a struct {
				ProblemID int64  `json:"problem_id"`
				Text      string `json:"text"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			id, err := c.Note(a.ProblemID, a.Text)
			if err != nil {
				return "", err
			}
			return jsonText(D{"id": id})
		}},
	{"dlg_parse", "Extract per-point positions (AGREE/DISAGREE per [P1]) from a stored review.",
		schema([]string{"review_id"}, D{"review_id": D{"type": "integer", "description": "review id from dlg_review or dlg_status"}}),
		func(c Core, raw json.RawMessage) (string, error) {
			var a struct {
				ReviewID int64 `json:"review_id"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			v, err := c.Parse(a.ReviewID)
			if err != nil {
				return "", err
			}
			return jsonText(v)
		}},
}

// handleMCP answers one request; a nil result means no reply (a notification).
func handleMCP(c Core, req rpcRequest) (result any, rpcErr *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		version := mcpLatest
		for _, v := range mcpVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return D{"protocolVersion": version, "capabilities": D{"tools": D{}},
			"serverInfo":   D{"name": "dlg", "version": versionString()},
			"instructions": "Decision ledger. Use dlg_handoff to get what to review and dlg_review to submit the reply."}, nil
	case "ping":
		return D{}, nil
	case "tools/list":
		return D{"tools": mcpTools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage("{}")
		}
		for _, t := range mcpTools {
			if t.Name != p.Name {
				continue
			}
			text, err := t.call(c, p.Arguments)
			if err != nil { // tool failures are results the model can read, not protocol errors
				return D{"content": []D{{"type": "text", "text": err.Error()}}, "isError": true}, nil
			}
			return D{"content": []D{{"type": "text", "text": text}}, "isError": false}, nil
		}
		return nil, &rpcError{-32602, "unknown tool: " + p.Name}
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

// serveMCP reads requests from r until it ends and writes responses to w.
func serveMCP(r io.Reader, w io.Writer, c Core) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	reply := func(id json.RawMessage, result any, e *rpcError) error {
		msg := D{"jsonrpc": "2.0", "id": id}
		if e != nil {
			msg["error"] = e
		} else {
			msg["result"] = result
		}
		b, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", b)
		return err
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if err := reply(json.RawMessage("null"), nil, &rpcError{-32700, "parse error"}); err != nil {
				return err
			}
			continue
		}
		result, rpcErr := handleMCP(c, req)
		if len(req.ID) == 0 { // notification: never answered
			continue
		}
		if err := reply(req.ID, result, rpcErr); err != nil {
			return err
		}
	}
	return sc.Err()
}
