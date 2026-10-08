package packagelayout

import "fmt"

// This file declares the per-platform companion-path contract, which is an installation
// contract rather than part of the archive layout or of the connector's behaviour.
//
// The connector resolves its packaged private companion the same way on every platform:
// ../private/bridge/lip-cursor-sdk-bridge[.exe] relative to the running outer executable.
// Whether that path lands on the installed launcher or on nothing at all is decided by the
// host, not by this plugin. Measured against the released Go-LIP v0.1.0 host binary, the
// host verifies the installed executable's digest and then either execs it in place
// (linux/amd64) or launches a private staged copy of it from a digest-addressed staging
// directory (windows/amd64), and only the first leaves a private/ tree beside the process.
//
// Those two measurements are now the adopted installation contract per platform: an operator
// who configures nothing still reaches the Cursor SDK on one platform, and has to name the
// installed launcher in bridge_executable on the other. The contract lives here, in the
// package the release metadata, the shipped record, the certification gate, and the
// operator documentation all already read, because a contract stated in four places is a
// contract that will eventually be stated four different ways.

// CompanionContract is one declared platform's supported companion-path configuration.
//
// The two values are the whole vocabulary. There is no third one on purpose: "unknown" and
// "try the other one" are the answers a platform reaches by not having been measured, and
// both read as an instruction an operator can follow and be wrong by.
type CompanionContract string

const (
	// CompanionPackagedDefault marks a platform where the packaged default resolution
	// works with no operator action, so bridge_executable may be set but need not be.
	CompanionPackagedDefault CompanionContract = "packaged-default-supported"

	// CompanionExplicitBridgeExecutable marks a platform where the packaged default cannot
	// reach the installed launcher, so a supported configuration names it explicitly and
	// an unset value fails as an explicit prerequisite naming that field.
	CompanionExplicitBridgeExecutable CompanionContract = "explicit-bridge_executable-required"
)

// companionContracts is the accepted vocabulary, so validation and any future switch over
// the two values have one place to agree with.
var companionContracts = []CompanionContract{CompanionPackagedDefault, CompanionExplicitBridgeExecutable}

// ParseCompanionContract reads one declared contract, rejecting anything outside the
// vocabulary rather than defaulting: a record that named a contract no operator is
// documented to run would be worse than one that failed to render.
func ParseCompanionContract(value CompanionContract) (CompanionContract, error) {
	for _, contract := range companionContracts {
		if value == contract {
			return contract, nil
		}
	}
	return "", fmt.Errorf("packagelayout: companion_contract %q is not one of %s and %s; a platform has to adopt "+
		"one of the two measured contracts, because an unmeasured platform has no supported installation to "+
		"document", value, CompanionPackagedDefault, CompanionExplicitBridgeExecutable)
}

// PackagedDefaultSupported reports whether an operator who configures nothing still reaches
// the packaged companion on this platform.
func (c CompanionContract) PackagedDefaultSupported() bool { return c == CompanionPackagedDefault }

// BridgeExecutableRequired reports whether a supported configuration has to name the
// packaged launcher in bridge_executable.
func (c CompanionContract) BridgeExecutableRequired() bool {
	return c == CompanionExplicitBridgeExecutable
}
