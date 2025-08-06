/*
Initium Analytics - A lightweight, self-hosted web analytics server

This application provides:
- Privacy-friendly page view tracking
- Real-time analytics dashboard
- Browser usage statistics
- Session tracking
- File-based data storage (JSON)

Author: Jason Cameron
Version: 1.0
*/
package main

import (
	"bytes"
	"encoding/json"   // For JSON marshaling/unmarshaling
	"fmt"             // For string formatting and printing
	"html/template"   // For rendering HTML templates
	"log"             // For logging errors and info
	"net/http"        // For HTTP server functionality
	"os"              // For file operations and environment variables
	"path/filepath"   // For cross-platform file path operations
	"sort"            // For sorting slices
	"strings"         // For string manipulation
	"sync"            // For thread-safe operations
	"time"            // For timestamp handling

	"github.com/gorilla/mux" // HTTP router for URL routing
)

// =============================================================================
// DATA STRUCTURES
// =============================================================================

// Website represents a registered website that can be tracked
// Each website has a unique ID used for tracking validation
type Website struct {
	ID     string `json:"id"`     // Unique identifier for tracking (e.g., "my-website")
	Domain string `json:"domain"` // Domain name (e.g., "localhost", "example.com")
	Name   string `json:"name"`   // Human-readable name (e.g., "My Blog")
}

// PageView represents a single page visit with all tracking data
// This is the core data structure for analytics tracking
type PageView struct {
	ID        string    `json:"id"`         // Unique ID for this page view
	WebsiteID string    `json:"website_id"` // Links to Website.ID for validation
	SessionID string    `json:"session_id"` // Browser session identifier
	PageURL   string    `json:"page_url"`   // Full URL of the visited page
	PageTitle string    `json:"page_title"` // HTML title of the page
	Referrer  string    `json:"referrer"`   // URL that referred the user (if any)
	IPAddress string    `json:"ip_address"` // Visitor's IP address
	UserAgent string    `json:"user_agent"` // Browser's user agent string
	Browser   string    `json:"browser"`    // Parsed browser name (Chrome, Firefox, etc.)
	Timestamp time.Time `json:"timestamp"`  // When the page view occurred
}

// Stats represents aggregated analytics data for API responses
// This structure is returned by the /stats/{trackingId} endpoint
type Stats struct {
	// Summary contains high-level metrics
	Summary struct {
		TotalViews      int `json:"total_views"`      // Total page views in time period
		UniqueSessions  int `json:"unique_sessions"`  // Number of unique visitor sessions
		DaysWithTraffic int `json:"days_with_traffic"` // Days that had at least one visit
	} `json:"summary"`
	
	// TopPages lists the most visited pages (limited to top 10)
	TopPages []struct {
		PageURL string `json:"page_url"` // URL of the page
		Views   int    `json:"views"`    // Number of views for this page
	} `json:"top_pages"`
	
	// Browsers lists browser usage statistics
	Browsers []struct {
		Browser string `json:"browser"` // Browser name (Chrome, Firefox, etc.)
		Count   int    `json:"count"`   // Number of visits from this browser
	} `json:"browsers"`
}

// =============================================================================
// GLOBAL CONFIGURATION
// =============================================================================

// Global variables for file paths and thread safety
var (
	// dataDir is the directory where all JSON data files are stored
	dataDir = "./data"
	
	// pageViewsFile stores all page view tracking data
	pageViewsFile = filepath.Join(dataDir, "pageviews.json")
	
	// websitesFile stores registered website configurations
	websitesFile = filepath.Join(dataDir, "websites.json")
	
	// mutex provides thread-safe access to JSON files
	// RWMutex allows multiple readers or one writer at a time
	mutex = &sync.RWMutex{}
)

// =============================================================================
// UTILITY FUNCTIONS
// =============================================================================

// ensureDataDir creates the data directory and initializes default data files
// This function is called on server startup to ensure the required file structure exists
func ensureDataDir() error {
	// Create the data directory if it doesn't exist
	// 0755 permissions: owner can read/write/execute, group/others can read/execute
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Initialize websites.json with a default website if it doesn't exist
	if _, err := os.Stat(websitesFile); os.IsNotExist(err) {
		// Create default website configuration
		websites := []Website{
			{ID: "my-website", Domain: "localhost", Name: "My Website"},
		}
		if err := writeJSONFile(websitesFile, websites); err != nil {
			return fmt.Errorf("failed to initialize websites file: %w", err)
		}
	}

	// Initialize pageviews.json with an empty array if it doesn't exist
	if _, err := os.Stat(pageViewsFile); os.IsNotExist(err) {
		if err := writeJSONFile(pageViewsFile, []PageView{}); err != nil {
			return fmt.Errorf("failed to initialize pageviews file: %w", err)
		}
	}

	return nil
}

// readJSONFile safely reads and unmarshals a JSON file into the provided interface
// Uses read lock to allow multiple concurrent reads
func readJSONFile(filename string, v interface{}) error {
	// Acquire read lock - multiple readers can access simultaneously
	mutex.RLock()
	defer mutex.RUnlock() // Ensure lock is released when function exits

	// Read the entire file into memory
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", filename, err)
	}
	
	// Parse JSON data into the provided interface
	return json.Unmarshal(data, v)
}

// writeJSONFile safely marshals and writes data to a JSON file
// Uses write lock to ensure exclusive access during writes
func writeJSONFile(filename string, v interface{}) error {
	// Acquire write lock - only one writer allowed, blocks all readers
	mutex.Lock()
	defer mutex.Unlock() // Ensure lock is released when function exits

	// Marshal data to pretty-printed JSON (2-space indentation)
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	
	// Write JSON data to file with 0644 permissions (owner read/write, group/others read)
	return os.WriteFile(filename, data, 0644)
}

// getBrowser attempts to identify the browser from the user-agent string
// It returns a simplified browser name (e.g., "Chrome", "Firefox")
func getBrowser(userAgent string) string {
	ua := strings.ToLower(userAgent)
	switch {
	case strings.Contains(ua, "chrome") && !strings.Contains(ua, "edg"):
		return "Chrome"
	case strings.Contains(ua, "firefox"):
		return "Firefox"
	case strings.Contains(ua, "safari") && !strings.Contains(ua, "chrome"):
		return "Safari"
	case strings.Contains(ua, "edg"):
		return "Edge"
	default:
		return "Other"
	}
}

// generateID creates a unique ID based on the current Unix timestamp
// This provides a simple, time-sortable unique identifier for page views
func generateID() string {
	// Combine nanoseconds and seconds for a highly unique ID
	return fmt.Sprintf("%d_%d", time.Now().UnixNano(), time.Now().Unix())
}

// getClientIP extracts the visitor's IP address from the HTTP request
// It prioritizes proxy headers like X-Forwarded-For and X-Real-IP
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header, common for proxies
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// The first IP in the list is the original client IP
		return strings.Split(xff, ",")[0]
	}
	// Check X-Real-IP header, another common proxy header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	// Fallback to the remote address from the TCP connection
	// This may be the proxy's IP, not the user's
	return strings.Split(r.RemoteAddr, ":")[0]
}

// =============================================================================
// HTTP HANDLERS
// =============================================================================

// trackHandler receives tracking data from the client-side JavaScript
// It validates the request and saves the page view to the JSON file
func trackHandler(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers to allow cross-origin requests
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	
	// Handle preflight OPTIONS request
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	
	// Only allow POST requests for tracking
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Define a temporary struct to decode incoming JSON data
	var data struct {
		TrackingID string `json:"tracking_id"`
		SessionID  string `json:"session_id"`
		PageURL    string `json:"page_url"`
		PageTitle  string `json:"page_title"`
		Referrer   string `json:"referrer"`
		UserAgent  string `json:"user_agent"`
		Timestamp  string `json:"timestamp"` // Received as string, then parsed
	}

	// Decode the JSON request body into the temporary struct
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// --- Validation Step ---
	// Verify that the tracking ID corresponds to a registered website
	var websites []Website
	if err := readJSONFile(websitesFile, &websites); err != nil {
		http.Error(w, "Server error: could not read websites file", http.StatusInternalServerError)
		return
	}

	// Check if the provided tracking ID exists
	found := false
	for _, website := range websites {
		if website.ID == data.TrackingID {
			found = true
			break
		}
	}

	if !found {
		http.Error(w, "Invalid tracking ID", http.StatusBadRequest)
		return
	}

	// --- Data Processing ---
	// Parse the timestamp string into a time.Time object
	timestamp, err := time.Parse(time.RFC3339, data.Timestamp)
	if err != nil {
		// Fallback to current server time if parsing fails
		timestamp = time.Now()
	}

	// Create a new PageView record from the validated data
	pageView := PageView{
		ID:        generateID(),
		WebsiteID: data.TrackingID,
		SessionID: data.SessionID,
		PageURL:   data.PageURL,
		PageTitle: data.PageTitle,
		Referrer:  data.Referrer,
		IPAddress: getClientIP(r),
		UserAgent: data.UserAgent,
		Browser:   getBrowser(data.UserAgent),
		Timestamp: timestamp,
	}

	// --- Data Storage ---
	// Read existing page views from the file
	var pageViews []PageView
	if err := readJSONFile(pageViewsFile, &pageViews); err != nil {
		// If file doesn't exist or is empty, initialize an empty slice
		pageViews = []PageView{}
	}

	// Append the new page view to the slice
	pageViews = append(pageViews, pageView)

	// Data Retention: Keep only the last 10,000 records to prevent the file from growing indefinitely
	if len(pageViews) > 10000 {
		pageViews = pageViews[len(pageViews)-10000:]
	}

	// Save the updated slice back to the JSON file
	if err := writeJSONFile(pageViewsFile, pageViews); err != nil {
		http.Error(w, "Server error: could not save page view", http.StatusInternalServerError)
		return
	}

	// Respond with a success message
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// statsHandler serves aggregated analytics data as a JSON response.
// It calculates stats for a given tracking ID over the last 30 days.
func statsHandler(w http.ResponseWriter, r *http.Request) {
	// Extract trackingId from the URL (e.g., /stats/my-website)
	vars := mux.Vars(r)
	trackingID := vars["trackingId"]

	// Read all page views from the data file
	var pageViews []PageView
	if err := readJSONFile(pageViewsFile, &pageViews); err != nil {
		http.Error(w, "Server error: could not read page views", http.StatusInternalServerError)
		return
	}

	// --- Data Aggregation ---
	// Filter page views for the requested website and within the last 30 days
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	var recentViews []PageView
	for _, pv := range pageViews {
		if pv.WebsiteID == trackingID && pv.Timestamp.After(thirtyDaysAgo) {
			recentViews = append(recentViews, pv)
		}
	}

	// Calculate statistics from the filtered page views
	totalViews := len(recentViews)
	sessionSet := make(map[string]bool)
	daySet := make(map[string]bool)
	pageStats := make(map[string]int)
	browserStats := make(map[string]int)

	for _, pv := range recentViews {
		sessionSet[pv.SessionID] = true
		daySet[pv.Timestamp.Format("2006-01-02")] = true
		pageStats[pv.PageURL]++
		browserStats[pv.Browser]++
	}

	// --- Response Building ---
	// Populate the Stats structure for the JSON response
	var stats Stats
	stats.Summary.TotalViews = totalViews
	stats.Summary.UniqueSessions = len(sessionSet)
	stats.Summary.DaysWithTraffic = len(daySet)

	// Aggregate and sort top pages (up to 10)
	type pageCount struct {
		URL   string
		Count int
	}
	var pages []pageCount
	for url, count := range pageStats {
		pages = append(pages, pageCount{URL: url, Count: count})
	}
	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Count > pages[j].Count
	})

	for i, page := range pages {
		if i >= 10 {
			break // Limit to top 10
		}
		stats.TopPages = append(stats.TopPages, struct {
			PageURL string `json:"page_url"`
			Views   int    `json:"views"`
		}{PageURL: page.URL, Views: page.Count})
	}

	// Aggregate and sort browser stats
	type browserCount struct {
		Browser string
		Count   int
	}
	var browsers []browserCount
	for browser, count := range browserStats {
		browsers = append(browsers, browserCount{Browser: browser, Count: count})
	}
	sort.Slice(browsers, func(i, j int) bool {
		return browsers[i].Count > browsers[j].Count
	})

	for _, browser := range browsers {
		stats.Browsers = append(stats.Browsers, struct {
			Browser string `json:"browser"`
			Count   int    `json:"count"`
		}{Browser: browser.Browser, Count: browser.Count})
	}

	// Send the response as JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// analyticsScriptHandler serves the dynamic JavaScript tracking file.
// It injects the correct tracking ID into the script.
func analyticsScriptHandler(w http.ResponseWriter, r *http.Request) {
	// Read website configuration to get the tracking ID
	var websites []Website
	if err := readJSONFile(websitesFile, &websites); err != nil || len(websites) == 0 {
		http.Error(w, "Analytics not configured", http.StatusInternalServerError)
		return
	}
	// Use the ID of the first website in the configuration
	trackingID := websites[0].ID

	// The tracking script, with a placeholder for the tracking ID
	scriptContent := `(function() {
    const Analytics = {
        endpoint: '{{ANALYTICS_ORIGIN}}/track',
        trackingId: '{{TRACKING_ID}}', // This will be replaced by the server
        
        init() {
            this.sessionId = this.getSessionId();
            this.trackPageView();
        },
        
        getSessionId() {
            let sessionId = sessionStorage.getItem('analytics_session');
            if (!sessionId) {
                sessionId = Date.now().toString(36) + Math.random().toString(36).substr(2, 5);
                sessionStorage.setItem('analytics_session', sessionId);
            }
            return sessionId;
        },
        
        trackPageView() {
            const data = {
                tracking_id: this.trackingId,
                session_id: this.sessionId,
                page_url: window.location.href,
                page_title: document.title,
                referrer: document.referrer,
                user_agent: navigator.userAgent,
                timestamp: new Date().toISOString()
            };
            
            // Use sendBeacon for reliable, asynchronous tracking
            if (navigator.sendBeacon) {
                const blob = new Blob([JSON.stringify(data)], {
                    type: 'application/json'
                });
                navigator.sendBeacon(this.endpoint, blob);
            } else {
                // Fallback to fetch for older browsers
                fetch(this.endpoint, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(data)
                }).catch(() => {});
            }
        }
    };
    
    // Run analytics script after the DOM is loaded
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', () => Analytics.init());
    } else {
        Analytics.init();
    }
})();`

	// Get the analytics server origin from the request
	analyticsOrigin := "http://" + r.Host
	if r.TLS != nil {
		analyticsOrigin = "https://" + r.Host
	}
	
	// Replace the placeholders with actual values
	finaScript := strings.Replace(scriptContent, "{{TRACKING_ID}}", trackingID, 1)
	finaScript = strings.Replace(finaScript, "{{ANALYTICS_ORIGIN}}", analyticsOrigin, 1)

	// Serve the final script
	w.Header().Set("Content-Type", "application/javascript")
	w.Write([]byte(finaScript))
}

// dashboardHandler serves the main analytics dashboard HTML page.
// It passes the tracking ID to the template for dynamic API calls.
func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	// Read website configuration to pass the tracking ID to the template
	var websites []Website
	if err := readJSONFile(websitesFile, &websites); err != nil || len(websites) == 0 {
		http.Error(w, "Analytics not configured", http.StatusInternalServerError)
		return
	}

	// Parse the dashboard template
	tmpl, err := template.ParseFiles("templates/dashboard.html")
	if err != nil {
		http.Error(w, "Error loading template", http.StatusInternalServerError)
		log.Printf("Error parsing dashboard template: %v", err)
		return
	}

	// Create data structure to pass to the template
	pageData := struct {
		TrackingID string
	}{
		TrackingID: websites[0].ID,
	}

	// Execute the template, passing in the tracking ID
	w.Header().Set("Content-Type", "text/html")
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, pageData); err != nil {
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
		log.Printf("Error executing dashboard template: %v", err)
		return
	}

	buf.WriteTo(w)
}

// testPageHandler serves a test page with analytics tracking enabled.
func testPageHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFiles("templates/test1.html")
	if err != nil {
		http.Error(w, "Error loading template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
	}
}

// testPage2Handler serves a second test page.
func testPage2Handler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFiles("templates/test2.html")
	if err != nil {
		http.Error(w, "Error loading template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
	}
}

// main is the entry point of the application.
// It sets up the server, routes, and middleware.
func main() {
	// Ensure the data directory and required files exist on startup
	if err := ensureDataDir(); err != nil {
		log.Fatalf("Failed to initialize data directory: %v", err)
	}

	// Create a new Gorilla Mux router
	// This router provides more advanced routing capabilities than the default http.ServeMux
	r := mux.NewRouter()

	// --- Route Definitions ---
	// Each route maps a URL path to a handler function
	r.HandleFunc("/", dashboardHandler).Methods("GET")
	r.HandleFunc("/track", trackHandler).Methods("POST")
	r.HandleFunc("/stats/{trackingId}", statsHandler).Methods("GET")
	r.HandleFunc("/analytics.js", analyticsScriptHandler).Methods("GET")
	r.HandleFunc("/test", testPageHandler).Methods("GET")
	r.HandleFunc("/test2", testPage2Handler).Methods("GET")

	// --- Middleware ---
	// This middleware adds security headers to all responses
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Prevents MIME-sniffing the content type
			w.Header().Set("X-Content-Type-Options", "nosniff")
			// Prevents the page from being displayed in a frame
			w.Header().Set("X-Frame-Options", "DENY")
			// Enables XSS filtering in browsers
			w.Header().Set("X-XSS-Protection", "1; mode=block")
			// Call the next handler in the chain
			next.ServeHTTP(w, r)
		})
	})

	// --- Server Startup ---
	// Use the PORT environment variable if available, otherwise default to 8080
	port := "8080"
	if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

	// Log server startup information
	fmt.Printf("🚀 Go Analytics server starting on http://localhost:%s\n", port)
	fmt.Printf("📊 Dashboard: http://localhost:%s\n", port)

	// Start the HTTP server
	// log.Fatal will print any server errors to stderr and exit the application
	log.Fatal(http.ListenAndServe(":"+port, r))
}
