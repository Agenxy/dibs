package main

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

const (
	publicationWorkflow = ".github/workflows/release-rehearsal.yml"
	publicationArtifact = "full-publication"
	publicationFile     = "full-publication.json"
	publicationBundle   = "full-publication.json.bundle"
	rehearsalWarning    = "REHEARSAL, not a Dibs release, do not install"
)

// The operator chose this ONE public, immutable-release repository on 2026-10-04.
// This reviewed source binding is not an environment variable, dispatch input,
// URL or signing-identity option. An empty or production binding still refuses.
// Tests substitute a private fixture binding; production has no setter.
var rehearsalRepository = "Agenxy/dibs-release-rehearsal"

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type publicationTarget struct {
	repository, workflow, tag string
	rehearsal                 bool
}

func productionTarget(c config) publicationTarget {
	return publicationTarget{repository, workflowPath, "v" + c.version, false}
}

func rehearsalTarget(c config) (publicationTarget, error) {
	if rehearsalRepository == "" {
		return publicationTarget{}, errors.New("full-publication rehearsal repository is not bound; " +
			"operator choice and reviewed source binding required")
	}
	if !repositoryPattern.MatchString(rehearsalRepository) || strings.EqualFold(rehearsalRepository, repository) {
		return publicationTarget{}, errors.New("rehearsal target must NEVER be the production repository")
	}
	return publicationTarget{rehearsalRepository, publicationWorkflow, "rehearsal-v" + c.version + "-" + c.sha, true}, nil
}

func resolveTarget(c config) (publicationTarget, error) {
	switch c.target {
	case "", "production":
		if c.negativeControl {
			return publicationTarget{}, errors.New("discovery negative control is rehearsal-only; NEVER production")
		}
		if c.phase == "full-publication" || c.phase == "full-publication-validate" ||
			c.phase == "prepare-rehearsal" {
			return publicationTarget{}, errors.New("scratch preparation and full-publication " +
				"require the closed rehearsal target")
		}
		return productionTarget(c), nil
	case "rehearsal":
		if c.phase != "full-publication" && c.phase != "full-publication-validate" && c.phase != "prepare-rehearsal" {
			return publicationTarget{}, errors.New("rehearsal target cannot enter a production phase")
		}
		return rehearsalTarget(c)
	default:
		return publicationTarget{}, errors.New("unknown release target; " +
			"choose production or rehearsal, never a repository or identity")
	}
}

func targetOf(c config) publicationTarget {
	if c.destination.repository != "" {
		return c.destination
	}
	return productionTarget(c)
}

func (d publicationTarget) identity() string {
	return "https://github.com/" + d.repository + "/" + d.workflow + "@refs/tags/" + d.tag
}

func rehearsalContext(c config) error {
	d := targetOf(c)
	if !d.rehearsal || os.Getenv("GITHUB_REPOSITORY") != d.repository ||
		os.Getenv("GITHUB_EVENT_NAME") != "workflow_dispatch" ||
		os.Getenv("GITHUB_REF") != "refs/tags/"+d.tag || os.Getenv("GITHUB_SHA") != c.sha ||
		!positiveID(c.runID) || !positiveID(c.attempt) {
		return errors.New("full-publication requires bound scratch repository, " +
			"unique exact-source tag and authenticated Actions run/attempt")
	}
	if c.rehearsal || c.deliveryRehearsal {
		return errors.New("full-publication cannot reuse a non-publishing preflight/delivery control")
	}
	for _, name := range []string{
		"DIBS_SIGNING_P12", "DIBS_SIGNING_P12_PASSWORD",
		"DIBS_SIGNING_KEYCHAIN", "DIBS_CODESIGN_IDENTITY", "HOMEBREW_TAP_DEPLOY_KEY",
	} {
		if os.Getenv(name) != "" {
			return errors.New("scratch rehearsal refuses production signing/tap credentials; remove " + name)
		}
	}
	return nil
}
