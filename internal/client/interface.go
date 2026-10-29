package client

// API defines the interface for API operations.
// This allows for mocking in tests.
type API interface {
	Get(path string) (*APIResponse, error)
	Post(path string, body any) (*APIResponse, error)
	PostText(path, text string) (*APIResponse, error)
	Patch(path string, body any) (*APIResponse, error)
	Put(path string, body any) (*APIResponse, error)
	Delete(path string) (*APIResponse, error)
	GetWithPagination(path string, fetchAll bool) (*APIResponse, error)
}

// Ensure Client implements API interface
var _ API = (*Client)(nil)
