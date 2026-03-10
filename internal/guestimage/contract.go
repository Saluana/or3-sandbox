package guestimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"or3-sandbox/internal/model"
)

const SidecarSuffix = ".or3.json"

type ControlContract struct {
	Mode                model.GuestControlMode `json:"mode"`
	ProtocolVersion     string                 `json:"protocol_version"`
	SupportedTransports []string               `json:"supported_transports,omitempty"`
}

type Contract struct {
	ContractVersion          string             `json:"contract_version"`
	ImagePath                string             `json:"image_path,omitempty"`
	ImageSHA256              string             `json:"image_sha256"`
	BuildVersion             string             `json:"build_version"`
	GitSHA                   string             `json:"git_sha,omitempty"`
	Profile                  model.GuestProfile `json:"profile"`
	Capabilities             []string           `json:"capabilities,omitempty"`
	AllowedFeatures          []string           `json:"allowed_features,omitempty"`
	Control                  ControlContract    `json:"control"`
	WorkspaceContractVersion string             `json:"workspace_contract_version"`
	SSHPresent               bool               `json:"ssh_present"`
	Dangerous                bool               `json:"dangerous,omitempty"`
	Debug                    bool               `json:"debug,omitempty"`
	PackageInventory         []string           `json:"package_inventory,omitempty"`
}

func SidecarPath(imagePath string) string {
	trimmed := strings.TrimSpace(imagePath)
	if trimmed == "" {
		return ""
	}
	return trimmed + SidecarSuffix
}

func Load(imagePath string) (Contract, error) {
	sidecarPath := SidecarPath(imagePath)
	if sidecarPath == "" {
		return Contract{}, fmt.Errorf("guest image path is required")
	}
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		return Contract{}, fmt.Errorf("read image contract %q: %w", sidecarPath, err)
	}
	var contract Contract
	if err := json.Unmarshal(data, &contract); err != nil {
		return Contract{}, fmt.Errorf("parse image contract %q: %w", sidecarPath, err)
	}
	if contract.ImagePath == "" {
		contract.ImagePath = filepath.Clean(imagePath)
	}
	contract.Capabilities = model.NormalizeFeatures(contract.Capabilities)
	contract.AllowedFeatures = model.NormalizeFeatures(contract.AllowedFeatures)
	return contract, nil
}

func Validate(imagePath string, contract Contract) error {
	if strings.TrimSpace(contract.ContractVersion) == "" {
		return fmt.Errorf("image contract is missing contract_version")
	}
	if !contract.Profile.IsValid() {
		return fmt.Errorf("image contract profile %q is invalid", contract.Profile)
	}
	if !contract.Control.Mode.IsValid() {
		return fmt.Errorf("image contract control mode %q is invalid", contract.Control.Mode)
	}
	if strings.TrimSpace(contract.Control.ProtocolVersion) == "" {
		return fmt.Errorf("image contract is missing control.protocol_version")
	}
	if strings.TrimSpace(contract.WorkspaceContractVersion) == "" {
		return fmt.Errorf("image contract is missing workspace_contract_version")
	}
	if strings.TrimSpace(contract.ImageSHA256) == "" {
		return fmt.Errorf("image contract is missing image_sha256")
	}
	actualSHA, err := ComputeSHA256(imagePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actualSHA, strings.TrimSpace(contract.ImageSHA256)) {
		return fmt.Errorf("image contract checksum mismatch for %q", imagePath)
	}
	if contract.Control.Mode == model.GuestControlModeAgent && contract.SSHPresent && !contract.Debug {
		return fmt.Errorf("agent-default image contract for %q must not advertise ssh unless debug=true", imagePath)
	}
	return nil
}

func ComputeSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read guest image %q: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("read guest image %q: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func RequestedFeaturesAllowed(contract Contract, requested []string) error {
	requested = model.NormalizeFeatures(requested)
	if len(requested) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(contract.AllowedFeatures))
	for _, value := range contract.AllowedFeatures {
		allowed[value] = struct{}{}
	}
	for _, value := range requested {
		if _, ok := allowed[value]; !ok {
			return fmt.Errorf("feature %q is not allowed by image profile %q", value, contract.Profile)
		}
	}
	return nil
}
