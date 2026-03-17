package guestimage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"or3-sandbox/internal/model"
)

func TestValidateRejectsAgentImageAdvertisingSSHWithoutDebug(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "guest.qcow2")
	if err := os.WriteFile(imagePath, []byte("guest"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	sha, err := ComputeSHA256(imagePath)
	if err != nil {
		t.Fatalf("compute sha: %v", err)
	}
	contract := Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                imagePath,
		ImageSHA256:              sha,
		BuildVersion:             "test",
		Profile:                  model.GuestProfileCore,
		Control:                  ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		SSHPresent:               true,
	}
	if err := Validate(imagePath, contract); err == nil || !strings.Contains(err.Error(), "must not advertise ssh") {
		t.Fatalf("expected ssh advertisement rejection, got %v", err)
	}
}

func TestValidateRejectsSSHCompatNonDebugProfile(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "guest.qcow2")
	if err := os.WriteFile(imagePath, []byte("guest"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	sha, err := ComputeSHA256(imagePath)
	if err != nil {
		t.Fatalf("compute sha: %v", err)
	}
	contract := Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                imagePath,
		ImageSHA256:              sha,
		BuildVersion:             "test",
		Profile:                  model.GuestProfileCore,
		Control:                  ControlContract{Mode: model.GuestControlModeSSHCompat, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		SSHPresent:               true,
	}
	if err := Validate(imagePath, contract); err == nil || !strings.Contains(err.Error(), "must use debug profile") {
		t.Fatalf("expected ssh-compat profile rejection, got %v", err)
	}
}

func TestRequestedFeaturesAllowedRejectsUnknownFeature(t *testing.T) {
	contract := Contract{Profile: model.GuestProfileContainer, AllowedFeatures: []string{"docker"}}
	if err := RequestedFeaturesAllowed(contract, []string{"docker", "gpu"}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected forbidden feature error, got %v", err)
	}
}

func TestLoadNormalizesCapabilitiesAndFeatures(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "guest.qcow2")
	if err := os.WriteFile(imagePath, []byte("guest"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	payload, err := json.Marshal(Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                imagePath,
		ImageSHA256:              strings.Repeat("0", 64),
		BuildVersion:             "test",
		Profile:                  model.GuestProfileRuntime,
		Capabilities:             []string{" Files ", "exec", "files"},
		AllowedFeatures:          []string{" Docker ", "docker"},
		Control:                  ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		Provenance: ProvenanceContract{
			BaseImageSource:         "https://example.invalid/base.qcow2",
			BaseImageSHA256:         strings.Repeat("1", 64),
			BaseImageExpectedSHA256: strings.Repeat("2", 64),
			ResolvedProfileSHA256:   strings.Repeat("3", 64),
			PackageInventorySHA256:  strings.Repeat("4", 64),
		},
	})
	if err != nil {
		t.Fatalf("marshal contract: %v", err)
	}
	if err := os.WriteFile(SidecarPath(imagePath), payload, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	contract, err := Load(imagePath)
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	if got := strings.Join(contract.Capabilities, ","); got != "exec,files" {
		t.Fatalf("unexpected normalized capabilities %q", got)
	}
	if got := strings.Join(contract.AllowedFeatures, ","); got != "docker" {
		t.Fatalf("unexpected normalized features %q", got)
	}
	if contract.Provenance.BaseImageExpectedSHA256 != strings.Repeat("2", 64) {
		t.Fatalf("unexpected provenance expected base image sha %q", contract.Provenance.BaseImageExpectedSHA256)
	}
}

func TestLoadPreservesProvenanceFields(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "guest.qcow2")
	if err := os.WriteFile(imagePath, []byte("guest"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	payload, err := json.Marshal(Contract{
		ContractVersion:          model.DefaultImageContractVersion,
		ImagePath:                imagePath,
		ImageSHA256:              strings.Repeat("0", 64),
		BuildVersion:             "test",
		Profile:                  model.GuestProfileCore,
		Control:                  ControlContract{Mode: model.GuestControlModeAgent, ProtocolVersion: model.DefaultGuestControlProtocolVersion},
		WorkspaceContractVersion: model.DefaultWorkspaceContractVersion,
		Provenance: ProvenanceContract{
			BaseImageSource:        "https://example.invalid/base.qcow2",
			BaseImageSHA256:        strings.Repeat("1", 64),
			ResolvedProfileSHA256:  strings.Repeat("2", 64),
			PackageInventorySHA256: strings.Repeat("3", 64),
		},
	})
	if err != nil {
		t.Fatalf("marshal contract: %v", err)
	}
	if err := os.WriteFile(SidecarPath(imagePath), payload, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	contract, err := Load(imagePath)
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	if contract.Provenance.BaseImageSource == "" || contract.Provenance.PackageInventorySHA256 == "" {
		t.Fatalf("expected provenance to survive load, got %#v", contract.Provenance)
	}
}
