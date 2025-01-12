package main

import (
	"maps"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/compute/v1"
)

type fakeGCPClient struct {
	fakeGetDisk       func(project, zone, name string) (*compute.Disk, error)
	fakeSetDiskLabels func(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error)
	fakeGetGCEOp      func(project, zone, name string) (*compute.Operation, error)

	setLabelsCalled bool
}

func (c *fakeGCPClient) GetDisk(project, zone, name string) (*compute.Disk, error) {
	if c.fakeGetDisk == nil {
		return nil, nil
	}
	return c.fakeGetDisk(project, zone, name)
}

func (c *fakeGCPClient) SetDiskLabels(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error) {
	c.setLabelsCalled = true
	if c.fakeSetDiskLabels == nil {
		return nil, nil
	}
	return c.fakeSetDiskLabels(project, zone, name, labelReq)
}

func (c *fakeGCPClient) GetGCEOp(project, zone, name string) (*compute.Operation, error) {
	if c.fakeSetDiskLabels == nil {
		return nil, nil
	}
	return c.fakeGetGCEOp(project, zone, name)
}

func setupFakeGCPClient(t *testing.T, currentLabels map[string]string, expectedSetLabels map[string]string) *fakeGCPClient {
	return &fakeGCPClient{
		fakeGetDisk: func(project, zone, name string) (*compute.Disk, error) {
			return &compute.Disk{Labels: currentLabels}, nil
		},
		fakeSetDiskLabels: func(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error) {
			if !maps.Equal(labelReq.Labels, expectedSetLabels) {
				t.Errorf("SetDiskLabels(), got labels = %v, want = %v", labelReq.Labels, expectedSetLabels)
			}
			return &compute.Operation{Status: "PENDING"}, nil
		},
		fakeGetGCEOp: func(project, zone, name string) (*compute.Operation, error) {
			return &compute.Operation{Status: "DONE"}, nil
		},
	}
}

func TestAddPDVolumeLabels(t *testing.T) {
	tests := []struct {
		name                  string
		volumeID              string
		currentLabels         map[string]string
		newPvcLabels          map[string]string
		expectSetLabelsCalled bool
		expectedSetLabels     map[string]string
	}{
		{
			name:                  "add new labels",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2"},
			newPvcLabels:          map[string]string{"foo": "bar", "dom.tld/key": "value"},
			expectSetLabelsCalled: true,
			expectedSetLabels:     map[string]string{"key1": "val1", "key2": "val2", "foo": "bar", "dom-tld_key": "value"},
		},
		{
			name:                  "labels already set",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2"},
			expectSetLabelsCalled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := setupFakeGCPClient(t, tt.currentLabels, tt.expectedSetLabels)

			addPDVolumeLabels(client, tt.volumeID, tt.newPvcLabels, "storage-ssd")

			if client.setLabelsCalled != tt.expectSetLabelsCalled {
				t.Error("SetDiskLabels() was not called")
			}
		})
	}
}

func TestDeletePDVolumeLabels(t *testing.T) {
	tests := []struct {
		name                  string
		volumeID              string
		currentLabels         map[string]string
		labelsToDelete        []string
		expectSetLabelsCalled bool
		expectedSetLabels     map[string]string
	}{
		{
			name:                  "delete existing labels",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2", "dom-tld_key": "bar"},
			labelsToDelete:        []string{"key1", "dom.tld/key"},
			expectSetLabelsCalled: true,
			expectedSetLabels:     map[string]string{"key2": "val2"},
		},
		{
			name:                  "no labels to delete",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2"},
			labelsToDelete:        []string{},
			expectSetLabelsCalled: false,
		},
		{
			name:                  "no matching labels to delete",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2"},
			labelsToDelete:        []string{"foo"},
			expectSetLabelsCalled: false,
		},
		{
			name:                  "all labels deleted",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1"},
			labelsToDelete:        []string{"key1"},
			expectSetLabelsCalled: true,
			expectedSetLabels:     map[string]string{},
		},
		{
			name:                  "no labels on disk",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         nil,
			labelsToDelete:        []string{"foo"},
			expectSetLabelsCalled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := setupFakeGCPClient(t, tt.currentLabels, tt.expectedSetLabels)

			deletePDVolumeLabels(client, tt.volumeID, tt.labelsToDelete, "storage-ssd")

			if client.setLabelsCalled != tt.expectSetLabelsCalled {
				t.Error("SetDiskLabels() was not called")
			}
		})
	}
}

func TestParseVolumeID(t *testing.T) {
	tests := []struct {
		name         string
		id           string
		wantProject  string
		wantLocation string
		wantName     string
		wantErr      bool
	}{
		{
			name:         "valid volume ID",
			id:           "projects/my-project/zones/us-central1/disks/my-disk",
			wantProject:  "my-project",
			wantLocation: "us-central1",
			wantName:     "my-disk",
			wantErr:      false,
		},
		{
			name:         "missing parts",
			id:           "projects/my-project/zones/",
			wantProject:  "",
			wantLocation: "",
			wantName:     "",
			wantErr:      true,
		},
		{
			name:         "empty input",
			id:           "",
			wantProject:  "",
			wantLocation: "",
			wantName:     "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project, location, name, err := parseVolumeID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseVolumeID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if project != tt.wantProject {
				t.Errorf("Expected project %q, got %q", tt.wantProject, project)
			}
			if location != tt.wantLocation {
				t.Errorf("Expected location %q, got %q", tt.wantLocation, location)
			}
			if name != tt.wantName {
				t.Errorf("Expected name %q, got %q", tt.wantName, name)
			}
		})
	}
}

func TestSanitizeKeyForGCP(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "standard valid key",
			key:  "app",
			want: "app",
		},
		{
			name: "uppercase converted to lowercase",
			key:  "APP",
			want: "app",
		},
		{
			name: "must start with letter",
			key:  "123-app",
			want: "k123-app",
		},
		{
			name: "replace invalid characters",
			key:  "kubernetes.io/app=name:v1",
			want: "kubernetes-io_app-name-v1",
		},
		{
			name: "international characters preserved",
			key:  "café-app",
			want: "café-app",
		},
		{
			name: "truncate long key",
			key:  strings.Repeat("a", 70),
			want: strings.Repeat("a", 63),
		},
		{
			name: "collapse multiple separators",
			key:  "app--name___test",
			want: "app-name_test",
		},
		{
			name: "trim trailing separators",
			key:  "app-name---_",
			want: "app-name",
		},
		{
			name: "empty string",
			key:  "",
			want: "",
		},
		{
			name: "only special characters",
			key:  "!@#$%^&*()",
			want: "",
		},
		{
			name: "preserve underscores",
			key:  "app_name_test",
			want: "app_name_test",
		},
		{
			name: "complex domain-style key",
			key:  "k8s.io/persistent-volume/mount-path",
			want: "k8s-io_persistent-volume_mount-path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeKeyForGCP(tt.key)
			if got != tt.want {
				t.Errorf("sanitizeKeyForGCP(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestSanitizeValueForGCP(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "standard valid value",
			value: "value-123",
			want:  "value-123",
		},
		{
			name:  "uppercase converted to lowercase",
			value: "VALUE_123",
			want:  "value_123",
		},
		{
			name:  "special characters replaced",
			value: "value.with:special/chars",
			want:  "value-with-special_chars",
		},
		{
			name:  "empty value allowed",
			value: "",
			want:  "",
		},
		{
			name:  "truncate long value",
			value: strings.Repeat("v", 70),
			want:  strings.Repeat("v", 63),
		},
		{
			name:  "collapse multiple separators",
			value: "value--with___separators",
			want:  "value-with_separators",
		},
		{
			name:  "trim trailing separators",
			value: "value-123---_",
			want:  "value-123",
		},
		{
			name:  "international characters preserved",
			value: "café-123",
			want:  "café-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeValueForGCP(tt.value)
			if got != tt.want {
				t.Errorf("sanitizeValueForGCP(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestSanitizeLabelsForGCP(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   map[string]string
	}{
		{
			name: "standard valid labels",
			labels: map[string]string{
				"app":    "nginx",
				"env":    "prod",
				"region": "us-east1",
			},
			want: map[string]string{
				"app":    "nginx",
				"env":    "prod",
				"region": "us-east1",
			},
		},
		{
			name: "sanitize keys and values",
			labels: map[string]string{
				"Kubernetes.io/app": "NGINX-1.0",
				"123-region":        "US-EAST1",
			},
			want: map[string]string{
				"kubernetes-io_app": "nginx-1-0",
				"k123-region":       "us-east1",
			},
		},
		{
			name: "handle empty values",
			labels: map[string]string{
				"app": "",
				"env": "prod",
				"":    "invalid",
			},
			want: map[string]string{
				"app": "",
				"env": "prod",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLabelsForGCP(tt.labels)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("sanitizeLabelsForGCP() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSanitizeKeysForGCP(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{
			name: "standard valid keys",
			keys: []string{"app", "env", "region"},
			want: []string{"app", "env", "region"},
		},
		{
			name: "sanitize invalid keys",
			keys: []string{
				"Kubernetes.io/app",
				"123-region",
				"",
				"!@#",
			},
			want: []string{
				"kubernetes-io_app",
				"k123-region",
			},
		},
		{
			name: "empty slice",
			keys: []string{},
			want: []string{},
		},
		{
			name: "all invalid keys",
			keys: []string{"", "!@#", "123"},
			want: []string{"k123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeKeysForGCP(tt.keys)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("sanitizeKeysForGCP() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
