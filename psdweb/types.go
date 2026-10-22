// Package psdweb exposes bounded, in-memory psdkit operations to browser workers and native tests.
package psdweb

import (
	"encoding/json/jsontext"
	"errors"
)

var (
	// ErrProtocol identifies an invalid bridge operation, handle, or parameter.
	ErrProtocol = errors.New("psdweb: invalid request")
	// ErrLimit identifies a bridge resource or transfer budget violation.
	ErrLimit = errors.New("psdweb: resource limit exceeded")
)

// Config bounds assets, output, retained handles and each library operation.
type Config struct {
	MaxBytes       int64 `json:"maxBytes"`
	MaxPixels      int64 `json:"maxPixels"`
	MaxAssets      int   `json:"maxAssets"`
	MaxResources   int   `json:"maxResources"`
	MaxJSONBytes   int   `json:"maxJSONBytes"`
	MaxOutputBytes int64 `json:"maxOutputBytes"`
}

// Request selects one documented operation; binary files travel separately from JSON.
type Request struct {
	Operation string            `json:"operation"`
	Target    string            `json:"target,omitempty"`
	Args      []jsontext.Value  `json:"args,omitempty"`
	Name      string            `json:"name,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	Alias     string            `json:"alias,omitempty"`
	Links     map[string]string `json:"links,omitempty"`
	TimeoutMS int               `json:"timeoutMS,omitempty"`
	// ImageOutput may be "png" to omit the duplicate raw RGBA transfer; empty returns both formats.
	ImageOutput string `json:"imageOutput,omitempty"`
}

// Fault is a structured bridge or original library error, never a substitute image.
type Fault struct {
	Code    string           `json:"code"`
	Message string           `json:"message"`
	Path    string           `json:"path,omitempty"`
	Feature string           `json:"feature,omitempty"`
	Offset  int64            `json:"offset,omitempty"`
	Audit   *ProjectionAudit `json:"audit,omitempty"`
}

// FileOutput names an encoded file without embedding bytes in JSON.
type FileOutput struct {
	Name, MIME string
	Data       []byte
}

// Binary is an owned output transferred independently of JSON and never base64 encoded on the wire.
type Binary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Length int    `json:"length"`
	Data   []byte `json:"-"`
}

// Response contains JSON metadata and separately transferable binary payloads.
type Response struct {
	OK       bool     `json:"ok"`
	Data     any      `json:"data,omitempty"`
	Error    *Fault   `json:"error,omitempty"`
	Binaries []Binary `json:"binaries,omitempty"`
}

// Parameter describes a visible, typed Go argument. Context, writers and resolvers are injected.
type Parameter struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Optional bool   `json:"optional,omitempty"`
	Example  any    `json:"example"`
	Schema   any    `json:"schema,omitempty"`
}

// Capability describes a callable operation and its original Go contract.
type Capability struct {
	ID          string      `json:"id"`
	Group       string      `json:"group"`
	Label       string      `json:"label"`
	GoAPI       string      `json:"goAPI"`
	Description string      `json:"description"`
	TargetKind  string      `json:"targetKind,omitempty"`
	Parameters  []Parameter `json:"parameters"`
	Policy      string      `json:"policy,omitempty"`
}
