package main

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"
	"unicode"

	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"google.golang.org/api/compute/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

var gcpLabelCharReplacer = strings.NewReplacer(
	// slash and dot are common, use different replacement chars:
	"/", "_", // replace slashes with underscores
	".", "-", // replace dots with dashes

	// less common characters, replace with dashes:
	" ", "-", // replace spaces with dashes
	":", "-", // replace colons with dashes
	",", "-", // replace commas with dashes
	";", "-", // replace semi-colons with dashes
	"=", "-", // replace equals with dashes
	"+", "-", // replace plus with dashes
)

type GCPClient interface {
	GetDisk(project, zone, name string) (*compute.Disk, error)
	SetDiskLabels(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error)
	GetGCEOp(project, zone, name string) (*compute.Operation, error)
}

type gcpClient struct {
	gce *compute.Service
}

func newGCPClient(ctx context.Context) (GCPClient, error) {
	client, err := compute.NewService(ctx)
	if err != nil {
		return nil, err
	}
	return &gcpClient{gce: client}, nil
}

func (c *gcpClient) GetDisk(project, zone, name string) (*compute.Disk, error) {
	return c.gce.Disks.Get(project, zone, name).Do()
}

func (c *gcpClient) SetDiskLabels(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error) {
	return c.gce.Disks.SetLabels(project, zone, name, labelReq).Do()
}

func (c *gcpClient) GetGCEOp(project, zone, name string) (*compute.Operation, error) {
	return c.gce.ZoneOperations.Get(project, zone, name).Do()
}

func addPDVolumeLabels(c GCPClient, volumeID string, labels map[string]string, storageclass string) {
	sanitizedLabels := sanitizeLabelsForGCP(labels)
	log.Debugf("labels to add to PD volume: %s: %s", volumeID, sanitizedLabels)

	project, location, name, err := parseVolumeID(volumeID)
	if err != nil {
		log.Error(err)
		return
	}
	disk, err := c.GetDisk(project, location, name)
	if err != nil {
		log.Error(err)
		return
	}

	// merge existing disk labels with new labels:
	updatedLabels := make(map[string]string)
	if disk.Labels != nil {
		updatedLabels = maps.Clone(disk.Labels)
	}
	maps.Copy(updatedLabels, sanitizedLabels)
	if maps.Equal(disk.Labels, updatedLabels) {
		log.Debug("labels already set on PD")
		return
	}

	req := &compute.ZoneSetLabelsRequest{
		Labels:           updatedLabels,
		LabelFingerprint: disk.LabelFingerprint,
	}
	op, err := c.SetDiskLabels(project, location, name, req)
	if err != nil {
		log.Errorf("failed to set labels on PD: %s", err)
		promActionsTotal.With(prometheus.Labels{"status": "error", "storageclass": storageclass}).Inc()
		return
	}

	waitForCompletion := func(_ context.Context) (bool, error) {
		resp, err := c.GetGCEOp(project, location, op.Name)
		if err != nil {
			return false, fmt.Errorf("failed to set labels on PD %s: %s", disk.Name, err)
		}
		return resp.Status == "DONE", nil
	}
	if err := wait.PollUntilContextTimeout(context.TODO(),
		time.Second,
		time.Minute,
		false,
		waitForCompletion); err != nil {
		log.Errorf("set label operation failed: %s", err)
		return
	}

	log.Debug("successfully set labels on PD")
	promActionsTotal.With(prometheus.Labels{"status": "success", "storageclass": storageclass}).Inc()
}

func deletePDVolumeLabels(c GCPClient, volumeID string, keys []string, storageclass string) {
	if len(keys) == 0 {
		return
	}
	sanitizedKeys := sanitizeKeysForGCP(keys)
	log.Debugf("labels to delete from PD volume: %s: %s", volumeID, sanitizedKeys)

	project, location, name, err := parseVolumeID(volumeID)
	if err != nil {
		log.Error(err)
		return
	}
	disk, err := c.GetDisk(project, location, name)
	if err != nil {
		log.Error(err)
		return
	}
	// if disk.Labels is nil, then there are no labels to delete
	if disk.Labels == nil {
		return
	}

	updatedLabels := maps.Clone(disk.Labels)
	for _, k := range sanitizedKeys {
		delete(updatedLabels, k)
	}
	if maps.Equal(disk.Labels, updatedLabels) {
		return
	}

	req := &compute.ZoneSetLabelsRequest{
		Labels:           updatedLabels,
		LabelFingerprint: disk.LabelFingerprint,
	}
	op, err := c.SetDiskLabels(project, location, name, req)
	if err != nil {
		log.Errorf("failed to delete labels from PD: %s", err)
		promActionsTotal.With(prometheus.Labels{"status": "error", "storageclass": storageclass}).Inc()
		return
	}

	waitForCompletion := func(_ context.Context) (bool, error) {
		resp, err := c.GetGCEOp(project, location, op.Name)
		if err != nil {
			return false, fmt.Errorf("failed to delete labels from PD %s: %s", disk.Name, err)
		}
		return resp.Status == "DONE", nil
	}
	if err := wait.PollUntilContextTimeout(context.TODO(),
		time.Second,
		time.Minute,
		false,
		waitForCompletion); err != nil {
		log.Errorf("delete label operation failed: %s", err)
		return
	}

	log.Debug("successfully deleted labels from PD")
	promActionsTotal.With(prometheus.Labels{"status": "success", "storageclass": storageclass}).Inc()
}

func parseVolumeID(id string) (string, string, string, error) {
	parts := strings.Split(id, "/")
	if len(parts) < 5 {
		return "", "", "", fmt.Errorf("invalid volume handle format")
	}
	project := parts[1]
	location := parts[3]
	name := parts[5]
	return project, location, name, nil
}

// isValidGCPChar returns true if the rune is valid for GCP labels:
// lowercase letters, numbers, dash, or underscore. International characters are
// allowed.
func isValidGCPChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_'
}

// sanitizeGCPLabelComponent handles the common sanitization logic for both keys
// and values
func sanitizeGCPLabelComponent(s string, isKey bool) string {
	// Convert to lowercase
	s = strings.ToLower(s)

	// Replace invalid characters with dashes
	s = gcpLabelCharReplacer.Replace(s)

	// Filter to only valid characters
	var b strings.Builder
	for _, r := range s {
		if isValidGCPChar(r) {
			b.WriteRune(r)
		}
	}
	s = b.String()

	// For keys, ensure they start with a letter
	if isKey && len(s) > 0 && !unicode.IsLetter(rune(s[0])) {
		s = "k" + s
	}

	// Remove consecutive dashes/underscores
	for strings.Contains(s, "--") || strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "--", "-")
		s = strings.ReplaceAll(s, "__", "_")
	}

	// Remove any trailing dashes or underscores
	s = strings.TrimRight(s, "-_")

	// Truncate to maximum length
	if len(s) > 63 {
		s = s[:63]
		s = strings.TrimRight(s, "-_")
	}

	return s
}

// sanitizeKeyForGCP sanitizes a Kubernetes label key to fit GCP's label key constraints:
// - Must start with a lowercase letter or international character
// - Can only contain lowercase letters, numbers, dashes and underscores
// - Must be between 1 and 63 characters long
// - Must use UTF-8 encoding
func sanitizeKeyForGCP(key string) string {
	return sanitizeGCPLabelComponent(key, true)
}

// sanitizeValueForGCP sanitizes a Kubernetes label value to fit GCP's label value constraints:
// - Can be empty
// - Maximum length of 63 characters
// - Can only contain lowercase letters, numbers, dashes and underscores
// - Must use UTF-8 encoding
func sanitizeValueForGCP(value string) string {
	return sanitizeGCPLabelComponent(value, false)
}

// sanitizeLabelsForGCP sanitizes a map of Kubernetes labels to fit GCP's constraints.
// Empty keys after sanitization are dropped from the result.
func sanitizeLabelsForGCP(labels map[string]string) map[string]string {
	if len(labels) > 64 {
		// If we have more than 64 labels, only take the first 64
		truncatedLabels := make(map[string]string, 64)
		i := 0
		for k, v := range labels {
			if i >= 64 {
				break
			}
			truncatedLabels[k] = v
			i++
		}
		labels = truncatedLabels
	}

	result := make(map[string]string, len(labels))
	for k, v := range labels {
		if sanitizedKey := sanitizeKeyForGCP(k); sanitizedKey != "" {
			result[sanitizedKey] = sanitizeValueForGCP(v)
		}
	}
	return result
}

// sanitizeKeysForGCP sanitizes a slice of label keys to fit GCP's constraints.
// Empty keys after sanitization are dropped from the result.
func sanitizeKeysForGCP(keys []string) []string {
	result := make([]string, 0, len(keys))
	for _, k := range keys {
		if sanitized := sanitizeKeyForGCP(k); sanitized != "" {
			result = append(result, sanitized)
		}
	}
	return result
}
