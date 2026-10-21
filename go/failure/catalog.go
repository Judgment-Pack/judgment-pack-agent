package failure

// The catalogue, written from contract/desk/catalog.json; catalog_test.go holds this file equal to it.

// CatalogVersion is the catalogue's version.
const CatalogVersion = 1

// Entry is one class of the catalogue: the sentence a person reads and the recovery it names.
type Entry struct {
	Message  string
	Recovery string
}

// Catalog is the failure catalogue, by class, in the catalogue's order (Codes).
var Catalog = map[string]Entry{
	"pace exceeded":      {Message: "The project period budget is exhausted. Wait for the next period or change the project policy.", Recovery: "wait"},
	"cancelled":          {Message: "The response was stopped.", Recovery: "none"},
	"request-limit":      {Message: "Work paused at the approved request limit.", Recovery: "increase"},
	"provider-limit":     {Message: "The AI provider stopped this run at its limit.", Recovery: "review"},
	"context-limit":      {Message: "The current AI context exceeds its configured budget. Saved work is retained; reduce the current request or read smaller context sections.", Recovery: "narrow"},
	"authentication":     {Message: "The AI connection needs authentication. Check its credentials or sign in again.", Recovery: "configure"},
	"permission":         {Message: "This AI connection does not allow the requested operation. Check access and enabled models.", Recovery: "configure"},
	"configuration":      {Message: "The AI connection is unavailable or needs configuration. Check it in Admin.", Recovery: "configure"},
	"busy":               {Message: "The AI connection is busy. No new operation was admitted.", Recovery: "wait"},
	"rate-limited":       {Message: "The AI provider is limiting requests. Wait before trying again.", Recovery: "wait"},
	"unavailable":        {Message: "The AI service is temporarily unavailable. Completed work is retained.", Recovery: "retry"},
	"connection-lost":    {Message: "The AI connection ended before completion. Review completed work before continuing.", Recovery: "review"},
	"timeout":            {Message: "The AI request timed out. Its outcome may be uncertain; review completed work before continuing.", Recovery: "review"},
	"invalid-request":    {Message: "The AI request was rejected. Check the selected model and request settings before trying again.", Recovery: "configure"},
	"invalid-response":   {Message: "The AI returned no usable complete response. Completed work is retained.", Recovery: "retry"},
	"storage":            {Message: "Progress could not be saved. Keep this window open and restore storage before continuing.", Recovery: "storage"},
	"stale-state":        {Message: "The saved context or configuration changed. Reload the current state before continuing.", Recovery: "reload"},
	"tool-failed":        {Message: "The tool could not complete this operation. Check its result before repeating it.", Recovery: "review"},
	"unknown":            {Message: "The response could not complete. Review completed work before continuing.", Recovery: "review"},
	"retries-exhausted":  {Message: "The AI connection could not recover after automatic retries. Review completed work before continuing.", Recovery: "review"},
	"invalid-tool-input": {Message: "The tool input needs correction before this operation can continue.", Recovery: "review"},
	"output-limit":       {Message: "The AI reached its output-token limit before completing the response. Completed work is retained.", Recovery: "review"},
	"content-filter":     {Message: "The AI provider blocked this response. Review the request before continuing.", Recovery: "review"},
	"repeated-work":      {Message: "The assistant kept repeating completed work. Desk stopped this response and retained the results.", Recovery: "review"},
}

// Codes are the catalogue's classes in its order.
var Codes = []string{"cancelled", "request-limit", "provider-limit", "context-limit", "authentication", "permission", "configuration", "busy", "rate-limited", "unavailable", "connection-lost", "timeout", "invalid-request", "invalid-response", "storage", "stale-state", "tool-failed", "unknown", "retries-exhausted", "invalid-tool-input", "output-limit", "content-filter", "repeated-work", "pace exceeded"}

// Aliases map a code a provider, the Desk or a search names to the class it is.
var Aliases = map[string]string{
	"busy":                     "busy",
	"assistant-relay-busy":     "busy",
	"sign-in-required":         "authentication",
	"assistant-no-key":         "authentication",
	"unauthorized":             "authentication",
	"assistant-key-unbound":    "configuration",
	"assistant-unconfigured":   "configuration",
	"model-not-allowed":        "permission",
	"forbidden":                "permission",
	"limit":                    "provider-limit",
	"unavailable":              "unavailable",
	"retries-exhausted":        "retries-exhausted",
	"run-failed":               "unknown",
	"assistant-relay-upstream": "connection-lost",
	"context_length_exceeded":  "context-limit",
	"context_window_exceeded":  "context-limit",
	"rate_limit_exceeded":      "rate-limited",
	"overloaded_error":         "unavailable",
	"authentication_error":     "authentication",
	"permission_error":         "permission",
	"permission_denied_error":  "permission",
	"invalid_api_key":          "authentication",
	"insufficient_quota":       "configuration",
	"billing_error":            "configuration",
	"stale":                    "stale-state",
	"credentials-required":     "authentication",
	"rate-limited":             "rate-limited",
	"search-temporary":         "unavailable",
	"search-timeout":           "timeout",
	"search-invalid-response":  "invalid-response",
	"search-invalid-request":   "invalid-request",
	"search-storage-failed":    "storage",
	"source-changed":           "stale-state",
	"blocked-by-policy":        "permission",
	"rate_limit_error":         "rate-limited",
	"RESOURCE_EXHAUSTED":       "rate-limited",
	"api_error":                "unavailable",
	"server_error":             "unavailable",
	"context-limit":            "context-limit",
}
