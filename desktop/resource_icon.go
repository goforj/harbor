package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"golang.org/x/net/html"
)

const (
	resourceIconRequestTimeout = 2 * time.Second
	resourceIconResponseLimit  = 1 << 20
)

// ResourceIconURL returns the icon declared by one reviewed project resource page.
func (a *App) ResourceIconURL(projectID string, resourceID string) (string, error) {
	typedProjectID := domain.ProjectID(projectID)
	if err := typedProjectID.Validate(); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	typedResourceID := domain.ResourceID(resourceID)
	if err := typedResourceID.Validate(); err != nil {
		return "", fmt.Errorf("resource: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return "", err
	}
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return "", fmt.Errorf("read Harbor snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return "", fmt.Errorf("validate Harbor snapshot: %w", err)
	}
	resource, err := findResource(snapshot, typedProjectID, typedResourceID)
	if err != nil {
		return "", err
	}

	iconURL, err := discoverResourceIcon(ctx, a.resourceIconClient, resource.URL)
	if err != nil {
		return "", fmt.Errorf("discover resource icon: %w", err)
	}
	return iconURL, nil
}

// discoverResourceIcon reads a reviewed local resource page without relying on webview CORS permissions.
func discoverResourceIcon(ctx context.Context, client *http.Client, resourceURL string) (string, error) {
	pageURL, err := url.Parse(resourceURL)
	if err != nil {
		return "", fmt.Errorf("parse resource URL: %w", err)
	}
	requestContext, cancel := context.WithTimeout(ctx, resourceIconRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, pageURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create resource page request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := client.Do(request)
	if err != nil {
		return "", nil
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", nil
	}

	href := declaredIconHref(io.LimitReader(response.Body, resourceIconResponseLimit))
	if href == "" {
		return "", nil
	}
	iconURL, err := pageURL.Parse(href)
	if err != nil {
		return "", nil
	}
	if iconURL.Scheme != "http" && iconURL.Scheme != "https" {
		return "", nil
	}
	if iconURL.Host != pageURL.Host {
		return "", nil
	}
	return iconURL.String(), nil
}

// declaredIconHref returns the first standard favicon declaration from one HTML document.
func declaredIconHref(source io.Reader) string {
	tokenizer := html.NewTokenizer(source)
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			return ""
		}
		if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken {
			continue
		}
		token := tokenizer.Token()
		if !strings.EqualFold(token.Data, "link") {
			continue
		}
		var href string
		var icon bool
		for _, attribute := range token.Attr {
			switch {
			case strings.EqualFold(attribute.Key, "href"):
				href = strings.TrimSpace(attribute.Val)
			case strings.EqualFold(attribute.Key, "rel"):
				icon = containsIconRelation(attribute.Val)
			}
		}
		if icon && href != "" {
			return href
		}
	}
}

// containsIconRelation recognizes the icon relation tokens browsers use for normal and Apple touch icons.
func containsIconRelation(value string) bool {
	for _, relation := range strings.Fields(value) {
		if strings.EqualFold(relation, "icon") || strings.EqualFold(relation, "apple-touch-icon") {
			return true
		}
	}
	return false
}
