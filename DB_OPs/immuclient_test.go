package DB_OPs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockImmuClient is a mock implementation of config.ImmuClient for testing
type MockImmuClient struct {
	mock.Mock
}

func (m *MockImmuClient) Scan(ctx context.Context, req *schema.ScanRequest) (*schema.Entries, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*schema.Entries), args.Error(1)
}

// Add other required methods from config.ImmuClient interface with empty implementations
func (m *MockImmuClient) Get(ctx context.Context, key []byte) (*schema.Entry, error) {
	return nil, nil
}

func (m *MockImmuClient) Set(ctx context.Context, key, value []byte) (*schema.TxHeader, error) {
	return nil, nil
}

func TestGetAllKeys(t *testing.T) {
	tests := []struct {
		name          string
		setupMock    func(m *MockImmuClient)
		expectedKeys  []string
		expectedError string
	}{
		{
			name: "successful empty result",
			setupMock: func(m *MockImmuClient) {
				m.On("Scan", mock.Anything, mock.MatchedBy(func(req *schema.ScanRequest) bool {
					return string(req.Prefix) == "test:" && req.Limit == uint64(1000) && req.SeekKey == nil
				})).Return(&schema.Entries{Entries: []*schema.Entry{}}, nil)
			},
			expectedKeys: []string{},
		},
		{
			name: "successful single batch",
			setupMock: func(m *MockImmuClient) {
				m.On("Scan", mock.Anything, mock.MatchedBy(func(req *schema.ScanRequest) bool {
					return string(req.Prefix) == "test:" && req.Limit == uint64(1000) && req.SeekKey == nil
				})).Return(&schema.Entries{
					Entries: []*schema.Entry{
						{Key: []byte("test:key1")},
						{Key: []byte("test:key2")},
					},
				}, nil)
			},
			expectedKeys: []string{"test:key1", "test:key2"},
		},
		{
			name: "successful multiple batches",
			setupMock: func(m *MockImmuClient) {
				// First batch
				m.On("Scan", mock.Anything, mock.MatchedBy(func(req *schema.ScanRequest) bool {
					return string(req.Prefix) == "test:" && req.Limit == uint64(1000) && req.SeekKey == nil
				})).Return(&schema.Entries{
					Entries: makeEntries(1000, "test:key"),
				}, nil)
				
				// Second batch (with seek key)
				m.On("Scan", mock.Anything, mock.MatchedBy(func(req *schema.ScanRequest) bool {
					return string(req.Prefix) == "test:" && req.Limit == uint64(1000) && string(req.SeekKey) == "test:key999"
				})).Return(&schema.Entries{
					Entries: []*schema.Entry{
						{Key: []byte("test:key1000")},
					},
				}, nil)
			},
			expectedKeys: append(
				makeKeyStrings(1000, "test:key"),
				"test:key1000",
			),
		},
		{
			name: "scan error",
			setupMock: func(m *MockImmuClient) {
				m.On("Scan", mock.Anything, mock.Anything).Return(nil, errors.New("scan error"))
			},
			expectedError: "scan error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockImmuClient{}
			if tt.setupMock != nil {
				tt.setupMock(mockClient)
			}

			keys, err := GetAllKeys(mockClient, "test:")

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedKeys, keys)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

// Helper functions for test data generation
func makeEntries(count int, prefix string) []*schema.Entry {
	entries := make([]*schema.Entry, count)
	for i := 0; i < count; i++ {
		entries[i] = &schema.Entry{
			Key: []byte(fmt.Sprintf("%s%d", prefix, i)),
		}
	}
	return entries
}

func makeKeyStrings(count int, prefix string) []string {
	keys := make([]string, count)
	for i := 0; i < count; i++ {
		keys[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return keys
}
