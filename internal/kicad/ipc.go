package kicad

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.nanomsg.org/mangos/v3/protocol/req"
	_ "go.nanomsg.org/mangos/v3/transport/ipc"
	"google.golang.org/protobuf/encoding/protowire"
)

// KiCad IPC API status codes from common/envelope.proto.
const (
	ApiStatusUnknown       = 0
	ApiStatusOK            = 1
	ApiStatusTimeout       = 2
	ApiStatusBadRequest    = 3
	ApiStatusNotReady      = 4
	ApiStatusUnhandled     = 5
	ApiStatusTokenMismatch = 6
	ApiStatusBusy          = 7
	ApiStatusUnimplemented = 8
	ApiStatusInternalError = 9
)

// KiCad FrameType enum values from common/types/base_types.proto.
const (
	FrameUnknown         = 0
	FrameProjectManager  = 1
	FrameSchematicEditor = 2
	FramePCBEditor       = 3
	FrameSpiceSimulator  = 4
	FrameSymbolEditor    = 5
	FrameFootprintEditor = 6
)

// KiCad DocumentType enum values from common/types/base_types.proto.
const (
	DocTypeUnknown   = 0
	DocTypeSchematic = 1
	DocTypeSymbol    = 2
	DocTypePCB       = 3
	DocTypeFootprint = 4
)

// KiCad PathType enum values from common/types/enums.proto.
const (
	PathUnknown           = 0
	PathUserPlugins       = 2
	PathUserTemplates     = 3
	PathUserSettings      = 11
	PathStockSymbols      = 15
	PathStockFootprints   = 16
	PathStockDesignBlocks = 17
	PathStock3DModels     = 18
	PathStockTemplates    = 23
)

// VersionInfo represents KiCad's version returned by GetVersion over IPC.
type VersionInfo struct {
	Major       uint32
	Minor       uint32
	Patch       uint32
	FullVersion string
}

// OpenDocument describes a document currently open in KiCad.
type OpenDocument struct {
	Type          int32
	BoardFilename string
	ProjectName   string
	ProjectPath   string
}

// IPCStatus captures the live connection status to a running KiCad instance.
type IPCStatus struct {
	Connected     bool
	SocketPath    string
	KiCadVersion  string
	OpenDocuments []OpenDocument
	Paths         map[int32]string
	LastError     string
}

// IPCClient communicates with a running KiCad 9/10 instance over its NNG/Protobuf Unix socket.
type IPCClient struct {
	mu         sync.Mutex
	socketPath string
	clientName string
	token      string
	timeout    time.Duration
}

// NewIPCClient creates an IPC client configured for socketPath.
func NewIPCClient(socketPath string) *IPCClient {
	token := os.Getenv("KICAD_API_TOKEN")
	return &IPCClient{
		socketPath: socketPath,
		clientName: fmt.Sprintf("kicad-lib-manager-%d", os.Getpid()),
		token:      token,
		timeout:    2 * time.Second,
	}
}

// SetSocketPath updates the target socket path.
func (c *IPCClient) SetSocketPath(socketPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.socketPath = socketPath
}

// DiscoverSocket resolves the configured socket path or looks for active KiCad sockets under /tmp/kicad/api*.sock.
func (c *IPCClient) DiscoverSocket() string {
	c.mu.Lock()
	configured := strings.TrimSpace(c.socketPath)
	c.mu.Unlock()

	if env := strings.TrimSpace(os.Getenv("KICAD_API_SOCKET")); env != "" {
		if _, err := os.Stat(strings.TrimPrefix(env, "ipc://")); err == nil {
			return env
		}
	}
	if configured != "" {
		clean := strings.TrimPrefix(configured, "ipc://")
		if _, err := os.Stat(clean); err == nil {
			return configured
		}
	}
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "kicad", "api*.sock"))
	for _, m := range matches {
		if _, err := os.Stat(m); err == nil {
			return m
		}
	}
	return configured
}

// CheckStatus connects to KiCad, queries GetVersion and open PCB/schematic documents, and returns IPCStatus.
func (c *IPCClient) CheckStatus() IPCStatus {
	sockPath := c.DiscoverSocket()
	st := IPCStatus{SocketPath: sockPath}

	if sockPath == "" {
		st.LastError = "no socket path configured"
		return st
	}
	clean := strings.TrimPrefix(sockPath, "ipc://")
	if _, err := os.Stat(clean); err != nil {
		st.LastError = "KiCad IPC socket not active (start KiCad with API server enabled)"
		return st
	}

	ver, err := c.GetVersionWithSocket(sockPath)
	if err != nil {
		if errPing := c.PingWithSocket(sockPath); errPing != nil {
			st.LastError = err.Error()
			return st
		}
		st.Connected = true
		st.KiCadVersion = "Connected"
	} else {
		st.Connected = true
		if ver.FullVersion != "" {
			st.KiCadVersion = ver.FullVersion
		} else {
			st.KiCadVersion = fmt.Sprintf("%d.%d.%d", ver.Major, ver.Minor, ver.Patch)
		}
	}

	if docs, err := c.GetOpenDocuments(sockPath, DocTypePCB); err == nil {
		st.OpenDocuments = append(st.OpenDocuments, docs...)
	}
	if docs, err := c.GetOpenDocuments(sockPath, DocTypeSchematic); err == nil {
		st.OpenDocuments = append(st.OpenDocuments, docs...)
	}
	if paths, err := c.GetPaths(sockPath); err == nil {
		st.Paths = paths
	}
	return st
}

// Ping sends `kiapi.common.commands.Ping` to verify the KiCad IPC server is responsive.
func (c *IPCClient) Ping() error {
	return c.PingWithSocket(c.DiscoverSocket())
}

// PingWithSocket sends `kiapi.common.commands.Ping` to an explicit socket path.
func (c *IPCClient) PingWithSocket(sockPath string) error {
	_, _, err := c.call(sockPath, "type.googleapis.com/kiapi.common.commands.Ping", nil)
	return err
}

// GetVersion queries the running KiCad instance version.
func (c *IPCClient) GetVersion() (*VersionInfo, error) {
	return c.GetVersionWithSocket(c.DiscoverSocket())
}

// GetVersionWithSocket queries GetVersion against an explicit socket path.
func (c *IPCClient) GetVersionWithSocket(sockPath string) (*VersionInfo, error) {
	_, payload, err := c.call(sockPath, "type.googleapis.com/kiapi.common.commands.GetVersion", nil)
	if err != nil {
		return nil, err
	}
	return decodeGetVersionResponse(payload)
}

// GetOpenDocuments queries open documents of the specified DocumentType.
func (c *IPCClient) GetOpenDocuments(sockPath string, docType int32) ([]OpenDocument, error) {
	var reqBytes []byte
	reqBytes = protowire.AppendTag(reqBytes, 1, protowire.VarintType)
	reqBytes = protowire.AppendVarint(reqBytes, uint64(docType))

	_, payload, err := c.call(sockPath, "type.googleapis.com/kiapi.common.commands.GetOpenDocuments", reqBytes)
	if err != nil {
		return nil, err
	}
	return decodeGetOpenDocumentsResponse(payload)
}

// GetPaths queries KiCad's well-known filesystem paths via `kiapi.common.commands.GetPaths`.
func (c *IPCClient) GetPaths(sockPath string) (map[int32]string, error) {
	_, payload, err := c.call(sockPath, "type.googleapis.com/kiapi.common.commands.GetPaths", nil)
	if err != nil {
		return nil, err
	}
	return decodeGetPathsResponse(payload), nil
}

// RefreshEditor sends RefreshEditor for a specific KiCad frame (e.g., FrameSchematicEditor, FramePCBEditor).
func (c *IPCClient) RefreshEditor(frameType int32) error {
	sockPath := c.DiscoverSocket()
	if sockPath == "" {
		return errors.New("KiCad IPC socket not configured")
	}
	var reqBytes []byte
	reqBytes = protowire.AppendTag(reqBytes, 1, protowire.VarintType)
	reqBytes = protowire.AppendVarint(reqBytes, uint64(frameType))
	_, _, err := c.call(sockPath, "type.googleapis.com/kiapi.common.commands.RefreshEditor", reqBytes)
	return err
}

// RefreshLibrariesAndNotify checks if KiCad IPC is active, pings it, and requests editor refresh notifications
// when libraries are updated on disk. Returns IPCStatus without failing if KiCad is offline.
func (c *IPCClient) RefreshLibrariesAndNotify() IPCStatus {
	st := c.CheckStatus()
	if !st.Connected {
		return st
	}
	for _, frame := range []int32{FrameProjectManager, FrameSchematicEditor, FramePCBEditor, FrameSymbolEditor, FrameFootprintEditor} {
		_ = c.RefreshEditor(frame)
	}
	return st
}

func (c *IPCClient) call(sockPath, typeURL string, payload []byte) (string, []byte, error) {
	if sockPath == "" {
		return "", nil, errors.New("empty KiCad IPC socket path")
	}
	dialURL := sockPath
	if !strings.Contains(dialURL, "://") {
		dialURL = "ipc://" + dialURL
	}

	sock, err := req.NewSocket()
	if err != nil {
		return "", nil, fmt.Errorf("create mangos req socket: %w", err)
	}
	defer sock.Close()

	_ = sock.SetOption("SEND-DEADLINE", c.timeout)
	_ = sock.SetOption("RECV-DEADLINE", c.timeout)

	if err := sock.Dial(dialURL); err != nil {
		return "", nil, fmt.Errorf("dial KiCad IPC %s: %w", dialURL, err)
	}

	c.mu.Lock()
	token := c.token
	clientName := c.clientName
	c.mu.Unlock()

	reqEnvelope := EncodeApiRequest(token, clientName, typeURL, payload)
	if err := sock.Send(reqEnvelope); err != nil {
		return "", nil, fmt.Errorf("send IPC request: %w", err)
	}

	rawResp, err := sock.Recv()
	if err != nil {
		return "", nil, fmt.Errorf("recv IPC response: %w", err)
	}

	resp, err := DecodeApiResponse(rawResp)
	if err != nil {
		return "", nil, err
	}
	if resp.KiCadToken != "" {
		c.mu.Lock()
		c.token = resp.KiCadToken
		c.mu.Unlock()
	}
	if resp.StatusCode != ApiStatusOK && resp.StatusCode != ApiStatusUnknown {
		return resp.TypeURL, resp.Payload, fmt.Errorf("KiCad IPC status %d: %s", resp.StatusCode, resp.ErrorMessage)
	}
	return resp.TypeURL, resp.Payload, nil
}

// DecodedApiResponse holds parsed fields from a KiCad `kiapi.common.ApiResponse` wire frame.
type DecodedApiResponse struct {
	KiCadToken   string
	StatusCode   int32
	ErrorMessage string
	TypeURL      string
	Payload      []byte
}

// EncodeApiRequest encodes a `kiapi.common.ApiRequest` protobuf wire message.
func EncodeApiRequest(token, clientName, typeURL string, payload []byte) []byte {
	var header []byte
	if token != "" {
		header = protowire.AppendTag(header, 1, protowire.BytesType)
		header = protowire.AppendString(header, token)
	}
	if clientName != "" {
		header = protowire.AppendTag(header, 2, protowire.BytesType)
		header = protowire.AppendString(header, clientName)
	}

	var anyMsg []byte
	if typeURL != "" {
		anyMsg = protowire.AppendTag(anyMsg, 1, protowire.BytesType)
		anyMsg = protowire.AppendString(anyMsg, typeURL)
	}
	if len(payload) > 0 {
		anyMsg = protowire.AppendTag(anyMsg, 2, protowire.BytesType)
		anyMsg = protowire.AppendBytes(anyMsg, payload)
	}

	var req []byte
	req = protowire.AppendTag(req, 1, protowire.BytesType)
	req = protowire.AppendBytes(req, header)
	req = protowire.AppendTag(req, 2, protowire.BytesType)
	req = protowire.AppendBytes(req, anyMsg)
	return req
}

// DecodeApiResponse decodes a `kiapi.common.ApiResponse` protobuf wire message.
func DecodeApiResponse(data []byte) (*DecodedApiResponse, error) {
	out := &DecodedApiResponse{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, fmt.Errorf("decode ApiResponse tag: %v", protowire.ParseError(n))
		}
		data = data[n:]
		switch num {
		case 1: // ApiResponseHeader
			b, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return nil, fmt.Errorf("decode ApiResponse.header: %v", protowire.ParseError(m))
			}
			data = data[m:]
			out.KiCadToken = decodeSingleStringField(b, 1)
		case 2: // ApiResponseStatus
			b, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return nil, fmt.Errorf("decode ApiResponse.status: %v", protowire.ParseError(m))
			}
			data = data[m:]
			out.StatusCode, out.ErrorMessage = decodeApiResponseStatus(b)
		case 3: // google.protobuf.Any
			b, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return nil, fmt.Errorf("decode ApiResponse.message: %v", protowire.ParseError(m))
			}
			data = data[m:]
			out.TypeURL, out.Payload = decodeAny(b)
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return nil, fmt.Errorf("skip ApiResponse field %d: %v", num, protowire.ParseError(m))
			}
			data = data[m:]
		}
	}
	return out, nil
}

// EncodeGetVersionResponse encodes a `kiapi.common.commands.GetVersionResponse` (used by tests & mock servers).
func EncodeGetVersionResponse(major, minor, patch uint32, fullVersion string) []byte {
	var ver []byte
	ver = protowire.AppendTag(ver, 1, protowire.VarintType)
	ver = protowire.AppendVarint(ver, uint64(major))
	ver = protowire.AppendTag(ver, 2, protowire.VarintType)
	ver = protowire.AppendVarint(ver, uint64(minor))
	ver = protowire.AppendTag(ver, 3, protowire.VarintType)
	ver = protowire.AppendVarint(ver, uint64(patch))
	if fullVersion != "" {
		ver = protowire.AppendTag(ver, 4, protowire.BytesType)
		ver = protowire.AppendString(ver, fullVersion)
	}

	var resp []byte
	resp = protowire.AppendTag(resp, 1, protowire.BytesType)
	resp = protowire.AppendBytes(resp, ver)
	return resp
}

// EncodeApiResponse encodes a `kiapi.common.ApiResponse` wire frame (used by tests & mock servers).
func EncodeApiResponse(token string, statusCode int32, errMsg, typeURL string, payload []byte) []byte {
	var header []byte
	if token != "" {
		header = protowire.AppendTag(header, 1, protowire.BytesType)
		header = protowire.AppendString(header, token)
	}

	var status []byte
	status = protowire.AppendTag(status, 1, protowire.VarintType)
	status = protowire.AppendVarint(status, uint64(statusCode))
	if errMsg != "" {
		status = protowire.AppendTag(status, 2, protowire.BytesType)
		status = protowire.AppendString(status, errMsg)
	}

	var anyMsg []byte
	if typeURL != "" {
		anyMsg = protowire.AppendTag(anyMsg, 1, protowire.BytesType)
		anyMsg = protowire.AppendString(anyMsg, typeURL)
	}
	if len(payload) > 0 {
		anyMsg = protowire.AppendTag(anyMsg, 2, protowire.BytesType)
		anyMsg = protowire.AppendBytes(anyMsg, payload)
	}

	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	out = protowire.AppendBytes(out, header)
	out = protowire.AppendTag(out, 2, protowire.BytesType)
	out = protowire.AppendBytes(out, status)
	out = protowire.AppendTag(out, 3, protowire.BytesType)
	out = protowire.AppendBytes(out, anyMsg)
	return out
}

func decodeSingleStringField(data []byte, targetNum protowire.Number) string {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return ""
		}
		data = data[n:]
		if num == targetNum && typ == protowire.BytesType {
			s, m := protowire.ConsumeString(data)
			if m >= 0 {
				return s
			}
			return ""
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			return ""
		}
		data = data[m:]
	}
	return ""
}

func decodeApiResponseStatus(data []byte) (int32, string) {
	var code int32
	var msg string
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]
		switch num {
		case 1:
			v, m := protowire.ConsumeVarint(data)
			if m < 0 {
				return code, msg
			}
			code = int32(v)
			data = data[m:]
		case 2:
			s, m := protowire.ConsumeString(data)
			if m < 0 {
				return code, msg
			}
			msg = s
			data = data[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return code, msg
			}
			data = data[m:]
		}
	}
	return code, msg
}

func decodeAny(data []byte) (string, []byte) {
	var typeURL string
	var value []byte
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]
		switch num {
		case 1:
			s, m := protowire.ConsumeString(data)
			if m < 0 {
				return typeURL, value
			}
			typeURL = s
			data = data[m:]
		case 2:
			b, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return typeURL, value
			}
			value = b
			data = data[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return typeURL, value
			}
			data = data[m:]
		}
	}
	return typeURL, value
}

func decodeGetVersionResponse(data []byte) (*VersionInfo, error) {
	info := &VersionInfo{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, fmt.Errorf("decode GetVersionResponse tag: %v", protowire.ParseError(n))
		}
		data = data[n:]
		if num == 1 && typ == protowire.BytesType {
			verBytes, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return nil, fmt.Errorf("decode version: %v", protowire.ParseError(m))
			}
			data = data[m:]
			for len(verBytes) > 0 {
				vn, vt, k := protowire.ConsumeTag(verBytes)
				if k < 0 {
					break
				}
				verBytes = verBytes[k:]
				switch vn {
				case 1:
					v, l := protowire.ConsumeVarint(verBytes)
					if l < 0 {
						return info, nil
					}
					info.Major = uint32(v)
					verBytes = verBytes[l:]
				case 2:
					v, l := protowire.ConsumeVarint(verBytes)
					if l < 0 {
						return info, nil
					}
					info.Minor = uint32(v)
					verBytes = verBytes[l:]
				case 3:
					v, l := protowire.ConsumeVarint(verBytes)
					if l < 0 {
						return info, nil
					}
					info.Patch = uint32(v)
					verBytes = verBytes[l:]
				case 4:
					s, l := protowire.ConsumeString(verBytes)
					if l < 0 {
						return info, nil
					}
					info.FullVersion = s
					verBytes = verBytes[l:]
				default:
					l := protowire.ConsumeFieldValue(vn, vt, verBytes)
					if l < 0 {
						return info, nil
					}
					verBytes = verBytes[l:]
				}
			}
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			break
		}
		data = data[m:]
	}
	return info, nil
}

func decodeGetOpenDocumentsResponse(data []byte) ([]OpenDocument, error) {
	var docs []OpenDocument
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]
		if num == 1 && typ == protowire.BytesType {
			docBytes, m := protowire.ConsumeBytes(data)
			if m < 0 {
				break
			}
			data = data[m:]
			docs = append(docs, decodeDocumentSpecifier(docBytes))
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			break
		}
		data = data[m:]
	}
	return docs, nil
}

func decodeDocumentSpecifier(data []byte) OpenDocument {
	var doc OpenDocument
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]
		switch num {
		case 1:
			v, m := protowire.ConsumeVarint(data)
			if m < 0 {
				return doc
			}
			doc.Type = int32(v)
			data = data[m:]
		case 4:
			s, m := protowire.ConsumeString(data)
			if m < 0 {
				return doc
			}
			doc.BoardFilename = s
			data = data[m:]
		case 5:
			pb, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return doc
			}
			data = data[m:]
			doc.ProjectName = decodeSingleStringField(pb, 1)
			doc.ProjectPath = decodeSingleStringField(pb, 2)
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return doc
			}
			data = data[m:]
		}
	}
	return doc
}

// EncodeGetPathsResponse encodes a `kiapi.common.commands.GetPathsResponse` (for testing/mock servers).
func EncodeGetPathsResponse(paths map[int32]string) []byte {
	var resp []byte
	for pt, p := range paths {
		var entry []byte
		entry = protowire.AppendTag(entry, 1, protowire.VarintType)
		entry = protowire.AppendVarint(entry, uint64(pt))
		entry = protowire.AppendTag(entry, 2, protowire.BytesType)
		entry = protowire.AppendString(entry, p)

		resp = protowire.AppendTag(resp, 1, protowire.BytesType)
		resp = protowire.AppendBytes(resp, entry)
	}
	return resp
}

func decodeGetPathsResponse(data []byte) map[int32]string {
	out := make(map[int32]string)
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]
		if num == 1 && typ == protowire.BytesType {
			entryBytes, m := protowire.ConsumeBytes(data)
			if m < 0 {
				break
			}
			data = data[m:]
			pt, path := decodePathEntry(entryBytes)
			if path != "" {
				out[pt] = path
			}
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			break
		}
		data = data[m:]
	}
	return out
}

func decodePathEntry(data []byte) (int32, string) {
	var pt int32
	var path string
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]
		switch num {
		case 1:
			v, m := protowire.ConsumeVarint(data)
			if m < 0 {
				return pt, path
			}
			pt = int32(v)
			data = data[m:]
		case 2:
			s, m := protowire.ConsumeString(data)
			if m < 0 {
				return pt, path
			}
			path = s
			data = data[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return pt, path
			}
			data = data[m:]
		}
	}
	return pt, path
}
