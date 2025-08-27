package auth

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

// CertificateInfo holds parsed certificate details for display
type CertificateInfo struct {
	Subject      string
	Issuer       string
	SerialNumber string
	NotBefore    time.Time
	NotAfter     time.Time
	IsCA         bool
	DNSNames     []string
	KeyUsage     string
	ExtKeyUsage  []string
	
	// Custom extensions
	MemberID    string
	ComponentID string
	ComponentType string
	
	// Status
	IsValid     bool
	IsExpired   bool
	ExpiresIn   time.Duration
	ErrorMsg    string
}

// LoadCertificateInfo loads and parses certificate information from a file
func LoadCertificateInfo(certPath string) (*CertificateInfo, error) {
	// Read certificate file
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read certificate: %w", err)
	}

	// Parse PEM block
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	// Parse certificate
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	info := &CertificateInfo{
		Subject:      cert.Subject.String(),
		Issuer:       cert.Issuer.String(),
		SerialNumber: cert.SerialNumber.String(),
		NotBefore:    cert.NotBefore,
		NotAfter:     cert.NotAfter,
		IsCA:         cert.IsCA,
		DNSNames:     cert.DNSNames,
		IsValid:      true,
	}

	// Check expiry
	now := time.Now()
	if now.After(cert.NotAfter) {
		info.IsExpired = true
		info.IsValid = false
		info.ErrorMsg = "Certificate has expired"
	} else if now.Before(cert.NotBefore) {
		info.IsValid = false
		info.ErrorMsg = "Certificate not yet valid"
	} else {
		info.ExpiresIn = cert.NotAfter.Sub(now)
	}

	// Parse key usage
	info.KeyUsage = parseKeyUsage(cert.KeyUsage)
	
	// Parse extended key usage
	for _, ext := range cert.ExtKeyUsage {
		info.ExtKeyUsage = append(info.ExtKeyUsage, parseExtKeyUsage(ext))
	}

	// Extract custom extensions
	info.MemberID = extractFromSubject(cert.Subject, "O")
	info.ComponentID = extractFromSubject(cert.Subject, "CN")
	
	// Determine component type from CN
	if cert.IsCA {
		info.ComponentType = "CA"
	} else if contains(cert.Subject.String(), "coordinator") {
		info.ComponentType = "Coordinator"
	} else if contains(cert.Subject.String(), "node") {
		info.ComponentType = "Node"
	} else if contains(cert.Subject.String(), "cli") || contains(cert.Subject.String(), "client") {
		info.ComponentType = "Client"
	} else {
		info.ComponentType = "Unknown"
	}

	return info, nil
}

// VerifyCertificateChain verifies that a certificate was signed by a CA
func VerifyCertificateChain(certPath, caPath string) error {
	// Load CA certificate
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate: %w", err)
	}

	// Parse CA certificate
	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil {
		return fmt.Errorf("failed to decode CA PEM block")
	}

	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	// Load certificate to verify
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate: %w", err)
	}

	// Parse certificate
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Create certificate pool with CA
	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	// Verify certificate
	opts := x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}

	if _, err := cert.Verify(opts); err != nil {
		return fmt.Errorf("certificate verification failed: %w", err)
	}

	return nil
}

// CheckCertificateExpiry checks if a certificate is expired or expiring soon
func CheckCertificateExpiry(certPath string, warnDays int) (status string, daysLeft int, err error) {
	info, err := LoadCertificateInfo(certPath)
	if err != nil {
		return "", 0, err
	}

	now := time.Now()
	daysLeft = int(info.NotAfter.Sub(now).Hours() / 24)

	if info.IsExpired {
		return "EXPIRED", daysLeft, nil
	} else if daysLeft < warnDays {
		return "EXPIRING_SOON", daysLeft, nil
	} else {
		return "VALID", daysLeft, nil
	}
}

// parseKeyUsage converts key usage flags to readable string
func parseKeyUsage(usage x509.KeyUsage) string {
	var usages []string
	
	if usage&x509.KeyUsageDigitalSignature != 0 {
		usages = append(usages, "DigitalSignature")
	}
	if usage&x509.KeyUsageContentCommitment != 0 {
		usages = append(usages, "ContentCommitment")
	}
	if usage&x509.KeyUsageKeyEncipherment != 0 {
		usages = append(usages, "KeyEncipherment")
	}
	if usage&x509.KeyUsageDataEncipherment != 0 {
		usages = append(usages, "DataEncipherment")
	}
	if usage&x509.KeyUsageKeyAgreement != 0 {
		usages = append(usages, "KeyAgreement")
	}
	if usage&x509.KeyUsageCertSign != 0 {
		usages = append(usages, "CertSign")
	}
	if usage&x509.KeyUsageCRLSign != 0 {
		usages = append(usages, "CRLSign")
	}
	
	if len(usages) == 0 {
		return "None"
	}
	
	result := ""
	for i, u := range usages {
		if i > 0 {
			result += ", "
		}
		result += u
	}
	return result
}

// parseExtKeyUsage converts extended key usage to readable string
func parseExtKeyUsage(usage x509.ExtKeyUsage) string {
	switch usage {
	case x509.ExtKeyUsageAny:
		return "Any"
	case x509.ExtKeyUsageServerAuth:
		return "ServerAuth"
	case x509.ExtKeyUsageClientAuth:
		return "ClientAuth"
	case x509.ExtKeyUsageCodeSigning:
		return "CodeSigning"
	case x509.ExtKeyUsageEmailProtection:
		return "EmailProtection"
	case x509.ExtKeyUsageIPSECEndSystem:
		return "IPSECEndSystem"
	case x509.ExtKeyUsageIPSECTunnel:
		return "IPSECTunnel"
	case x509.ExtKeyUsageIPSECUser:
		return "IPSECUser"
	case x509.ExtKeyUsageTimeStamping:
		return "TimeStamping"
	case x509.ExtKeyUsageOCSPSigning:
		return "OCSPSigning"
	default:
		return fmt.Sprintf("Unknown(%d)", usage)
	}
}

// extractFromSubject extracts a specific field from subject
func extractFromSubject(subject interface{}, field string) string {
	str := fmt.Sprintf("%v", subject)
	// Simple extraction - this could be improved
	if field == "CN" && contains(str, "CN=") {
		start := indexOf(str, "CN=") + 3
		end := indexOfFrom(str, ",", start)
		if end == -1 {
			return str[start:]
		}
		return str[start:end]
	}
	if field == "O" && contains(str, "O=") {
		start := indexOf(str, "O=") + 2
		end := indexOfFrom(str, ",", start)
		if end == -1 {
			return str[start:]
		}
		return str[start:end]
	}
	return ""
}

// Helper functions
func contains(s, substr string) bool {
	return indexOf(s, substr) != -1
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func indexOfFrom(s, substr string, from int) int {
	for i := from; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}