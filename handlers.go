package main

import (
	"crypto/subtle"
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	_ "github.com/mattn/go-sqlite3"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS tickets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	status TEXT NOT NULL DEFAULT 'open',
	ticket_type TEXT NOT NULL,
	hospital_name TEXT NOT NULL,
	f3_name TEXT NOT NULL,
	ao TEXT,
	event_date TEXT,
	url TEXT,
	description TEXT NOT NULL,
	admin_notes TEXT DEFAULT '',
	attachment_path TEXT DEFAULT '',
	resolved_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_tickets_f3_name ON tickets(f3_name);
CREATE INDEX IF NOT EXISTS idx_tickets_status ON tickets(status);
CREATE INDEX IF NOT EXISTS idx_tickets_created_at ON tickets(created_at);
`

type App struct {
	DB            *sql.DB
	DataDir       string
	AdminPassword string
	templatesDir  string
}

func NewApp(dataDir, adminPassword string) (*App, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "f3help.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, err
	}
	// Migration: add attachment_path column if it doesn't exist yet.
	_, _ = db.Exec(`ALTER TABLE tickets ADD COLUMN attachment_path TEXT DEFAULT ''`)

	log.Println("SQLite initialized at", dbPath)
	templatesDir := getEnv("TEMPLATES_DIR", "templates")
	return &App{
		DB:            db,
		DataDir:       dataDir,
		AdminPassword: adminPassword,
		templatesDir:  templatesDir,
	}, nil
}

func (app *App) Close() error {
	if app.DB != nil {
		return app.DB.Close()
	}
	return nil
}

// ── HTTP Basic Auth middleware ──────────────────────────────────────────────
// All /admin routes are wrapped with this. Username is ignored; only the
// password is checked. The browser shows its built-in credential dialog.
func (app *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(password), []byte(app.AdminPassword)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="F3 Denver Admin"`)
			http.Error(w, "Admin access required — enter your admin password.", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// Ticket represents a help desk ticket.
type Ticket struct {
	ID             int64
	CreatedAt      string
	UpdatedAt      string
	Status         string
	TicketType     string
	HospitalName   string
	F3Name         string
	AO             string
	EventDate      string
	URL            string
	Description    string
	AdminNotes     string
	AttachmentPath string
	ResolvedAt     *string
}

// canClose returns true when a ticket is in a state the user is allowed to close.
func canClose(status string) bool {
	return status == "open" || status == "in_progress"
}

func (app *App) SubmitFormHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	app.renderTemplate(w, "submit.html", map[string]interface{}{"AOs": AOsList})
}

func (app *App) SubmitTicketHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Parse up to 15 MB (screenshot may be several MB).
	if err := r.ParseMultipartForm(15 << 20); err != nil {
		if err2 := r.ParseForm(); err2 != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
	}

	hospitalName := strings.TrimSpace(r.FormValue("hospital_name"))
	f3Name := strings.TrimSpace(r.FormValue("f3_name"))
	ticketType := strings.TrimSpace(r.FormValue("ticket_type"))
	ao := strings.TrimSpace(r.FormValue("ao"))
	eventDate := strings.TrimSpace(r.FormValue("event_date"))
	ticketURL := strings.TrimSpace(r.FormValue("url"))
	description := strings.TrimSpace(r.FormValue("description"))

	renderErr := func(msg string) {
		app.renderTemplate(w, "submit.html", map[string]interface{}{"Error": msg, "AOs": AOsList})
	}

	if hospitalName == "" || f3Name == "" || description == "" {
		renderErr("Hospital name, F3 name, and description are required.")
		return
	}
	switch ticketType {
	case "preblast", "backblast":
		if ao == "" || eventDate == "" {
			renderErr("AO and event date are required for preblast/backblast.")
			return
		}
	case "yeti":
		if ao == "" {
			renderErr("AO is required for Yeti tracking.")
			return
		}
	case "website":
		if ticketURL == "" {
			renderErr("URL is required for website issues.")
			return
		}
	case "other":
		// no extra required
	default:
		renderErr("Invalid ticket type.")
		return
	}

	res, err := app.DB.Exec(
		`INSERT INTO tickets (ticket_type, hospital_name, f3_name, ao, event_date, url, description) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ticketType, hospitalName, f3Name, nullEmpty(ao), nullEmpty(eventDate), nullEmpty(ticketURL), description,
	)
	if err != nil {
		log.Printf("insert ticket: %v", err)
		http.Error(w, "Failed to save ticket", http.StatusInternalServerError)
		return
	}
	id, _ := res.LastInsertId()

	// Handle optional screenshot upload (all ticket types).
	if attachPath, err := app.saveScreenshot(r, id); err != nil {
		log.Printf("save screenshot for ticket %d: %v", id, err)
	} else if attachPath != "" {
		if _, err := app.DB.Exec(`UPDATE tickets SET attachment_path = ? WHERE id = ?`, attachPath, id); err != nil {
			log.Printf("update attachment_path for ticket %d: %v", id, err)
		}
	}

	http.Redirect(w, r, "/ticket/"+formatID(id), http.StatusSeeOther)
}

// saveScreenshot validates and writes the uploaded file.
// Returns the relative path ("attachments/{id}/screenshot.ext") or "" if no file.
func (app *App) saveScreenshot(r *http.Request, ticketID int64) (string, error) {
	file, header, err := r.FormFile("screenshot")
	if err != nil {
		return "", nil // no file uploaded
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read header: %w", err)
	}
	mime := http.DetectContentType(buf[:n])
	if mime != "image/jpeg" && mime != "image/png" {
		return "", fmt.Errorf("unsupported file type: %s (only JPEG and PNG allowed)", mime)
	}

	ext := ".jpg"
	if mime == "image/png" {
		ext = ".png"
	}

	dir := filepath.Join(app.DataDir, "attachments", formatID(ticketID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	_ = header
	dest := filepath.Join(dir, "screenshot"+ext)
	out, err := os.Create(dest)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	if _, err := out.Write(buf[:n]); err != nil {
		return "", fmt.Errorf("write buf: %w", err)
	}
	if _, err := io.Copy(out, file); err != nil {
		return "", fmt.Errorf("write rest: %w", err)
	}

	return filepath.Join("attachments", formatID(ticketID), "screenshot"+ext), nil
}

// deleteAttachment removes the image file and clears attachment_path in the DB.
func (app *App) deleteAttachment(ticketID int64, attachPath string) {
	if attachPath == "" {
		return
	}
	fullPath := filepath.Join(app.DataDir, attachPath)
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		log.Printf("delete attachment %s: %v", fullPath, err)
	}
	_ = os.Remove(filepath.Dir(fullPath)) // remove dir if empty
	if _, err := app.DB.Exec(`UPDATE tickets SET attachment_path = '' WHERE id = ?`, ticketID); err != nil {
		log.Printf("clear attachment_path for ticket %d: %v", ticketID, err)
	}
}

func (app *App) TicketsLookupHandler(w http.ResponseWriter, r *http.Request) {
	f3Name := strings.TrimSpace(r.FormValue("f3_name"))
	if f3Name == "" {
		app.renderTemplate(w, "tickets.html", map[string]interface{}{"Tickets": nil, "F3Name": ""})
		return
	}
	rows, err := app.DB.Query(
		`SELECT id, created_at, updated_at, status, ticket_type, hospital_name, f3_name, ao, event_date, url, description, admin_notes, attachment_path, resolved_at FROM tickets WHERE f3_name = ? ORDER BY created_at DESC`,
		f3Name,
	)
	if err != nil {
		log.Printf("query tickets: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var tickets []Ticket
	for rows.Next() {
		var t Ticket
		var ao, eventDate, url, adminNotes, attachPath sql.NullString
		var resolvedAt sql.NullString
		if err := rows.Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt, &t.Status, &t.TicketType, &t.HospitalName, &t.F3Name, &ao, &eventDate, &url, &t.Description, &adminNotes, &attachPath, &resolvedAt); err != nil {
			continue
		}
		t.AO = ao.String
		t.EventDate = eventDate.String
		t.URL = url.String
		t.AdminNotes = adminNotes.String
		t.AttachmentPath = attachPath.String
		if resolvedAt.Valid {
			t.ResolvedAt = &resolvedAt.String
		}
		tickets = append(tickets, t)
	}
	app.renderTemplate(w, "tickets.html", map[string]interface{}{"Tickets": tickets, "F3Name": f3Name})
}

// TicketDetailHandler handles both viewing a ticket (GET) and closing it (POST …/close).
func (app *App) TicketDetailHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// ── Close action ──────────────────────────────────────────────
	if strings.HasSuffix(path, "/close") {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, strings.TrimSuffix(path, "/close"), http.StatusSeeOther)
			return
		}
		idStr := strings.TrimPrefix(strings.TrimSuffix(path, "/close"), "/ticket/")
		var id int64
		if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
			http.NotFound(w, r)
			return
		}
		// Only close tickets that aren't already done.
		var status string
		var attachPath sql.NullString
		err := app.DB.QueryRow(`SELECT status, attachment_path FROM tickets WHERE id = ?`, id).Scan(&status, &attachPath)
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		if canClose(status) {
			app.deleteAttachment(id, attachPath.String)
			_, _ = app.DB.Exec(
				`UPDATE tickets SET status = 'closed', resolved_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id,
			)
		}
		http.Redirect(w, r, "/ticket/"+formatID(id), http.StatusSeeOther)
		return
	}

	// ── View ticket ───────────────────────────────────────────────
	idStr := strings.TrimPrefix(path, "/ticket/")
	if idStr == "" || path == "/ticket" {
		http.Redirect(w, r, "/tickets", http.StatusSeeOther)
		return
	}
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		http.NotFound(w, r)
		return
	}
	var t Ticket
	var ao, eventDate, url, adminNotes, attachPath sql.NullString
	var resolvedAt sql.NullString
	err := app.DB.QueryRow(
		`SELECT id, created_at, updated_at, status, ticket_type, hospital_name, f3_name, ao, event_date, url, description, admin_notes, attachment_path, resolved_at FROM tickets WHERE id = ?`,
		id,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt, &t.Status, &t.TicketType, &t.HospitalName, &t.F3Name, &ao, &eventDate, &url, &t.Description, &adminNotes, &attachPath, &resolvedAt)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("ticket detail: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	t.AO = ao.String
	t.EventDate = eventDate.String
	t.URL = url.String
	t.AdminNotes = adminNotes.String
	t.AttachmentPath = attachPath.String
	if resolvedAt.Valid {
		t.ResolvedAt = &resolvedAt.String
	}
	app.renderTemplate(w, "ticket_detail.html", map[string]interface{}{
		"Ticket":   t,
		"CanClose": canClose(t.Status),
	})
}

func (app *App) TipsPageHandler(w http.ResponseWriter, r *http.Request) {
	tipsPath := filepath.Join(app.DataDir, "tips.md")
	body, err := os.ReadFile(tipsPath)
	if err != nil {
		body = []byte(defaultTipsMarkdown)
		_ = os.WriteFile(tipsPath, body, 0644)
	}
	extensions := parser.CommonExtensions | parser.AutoHeadingIDs
	p := parser.NewWithExtensions(extensions)
	doc := p.Parse(body)
	opts := html.RendererOptions{Flags: html.CommonFlags}
	renderer := html.NewRenderer(opts)
	htmlBytes := markdown.Render(doc, renderer)
	app.renderTemplate(w, "tips.html", map[string]interface{}{"TipsHTML": template.HTML(htmlBytes)})
}

func (app *App) AdminDashboardHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(
		`SELECT id, created_at, updated_at, status, ticket_type, hospital_name, f3_name, ao, event_date, url, description, admin_notes, attachment_path, resolved_at FROM tickets ORDER BY created_at DESC`,
	)
	if err != nil {
		log.Printf("admin list tickets: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var tickets []Ticket
	for rows.Next() {
		var t Ticket
		var ao, eventDate, url, adminNotes, attachPath sql.NullString
		var resolvedAt sql.NullString
		if err := rows.Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt, &t.Status, &t.TicketType, &t.HospitalName, &t.F3Name, &ao, &eventDate, &url, &t.Description, &adminNotes, &attachPath, &resolvedAt); err != nil {
			continue
		}
		t.AO = ao.String
		t.EventDate = eventDate.String
		t.URL = url.String
		t.AdminNotes = adminNotes.String
		t.AttachmentPath = attachPath.String
		if resolvedAt.Valid {
			t.ResolvedAt = &resolvedAt.String
		}
		tickets = append(tickets, t)
	}
	app.renderTemplate(w, "admin.html", map[string]interface{}{"Tickets": tickets})
}

func (app *App) AdminUpdateTicketHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/admin/ticket/")
	if idStr == "" {
		http.NotFound(w, r)
		return
	}
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		http.NotFound(w, r)
		return
	}

	status := r.FormValue("status")
	adminNotes := r.FormValue("admin_notes")
	if status != "" {
		valid := map[string]bool{"open": true, "in_progress": true, "resolved": true, "closed": true}
		if !valid[status] {
			status = "open"
		}
	}

	// Delete attachment when resolving or closing.
	if status == "resolved" || status == "closed" {
		var attachPath sql.NullString
		if err := app.DB.QueryRow(`SELECT attachment_path FROM tickets WHERE id = ?`, id).Scan(&attachPath); err == nil {
			app.deleteAttachment(id, attachPath.String)
		}
	}

	resolving := status == "resolved" || status == "closed"
	if status != "" && resolving {
		_, err := app.DB.Exec(`UPDATE tickets SET status = ?, admin_notes = ?, resolved_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, adminNotes, id)
		if err != nil {
			log.Printf("update ticket: %v", err)
		}
	} else if status != "" {
		_, err := app.DB.Exec(`UPDATE tickets SET status = ?, admin_notes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, adminNotes, id)
		if err != nil {
			log.Printf("update ticket: %v", err)
		}
	} else {
		_, _ = app.DB.Exec(`UPDATE tickets SET admin_notes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, adminNotes, id)
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (app *App) AdminTipsHandler(w http.ResponseWriter, r *http.Request) {
	tipsPath := filepath.Join(app.DataDir, "tips.md")
	if r.Method == http.MethodPost {
		body := r.FormValue("body")
		if err := os.WriteFile(tipsPath, []byte(body), 0644); err != nil {
			log.Printf("write tips.md: %v", err)
			app.renderTemplate(w, "admin_tips.html", map[string]interface{}{"Body": body, "Error": "Failed to save."})
			return
		}
		http.Redirect(w, r, "/tips", http.StatusSeeOther)
		return
	}
	body, _ := os.ReadFile(tipsPath)
	if len(body) == 0 {
		body = []byte(defaultTipsMarkdown)
		_ = os.WriteFile(tipsPath, body, 0644)
	}
	app.renderTemplate(w, "admin_tips.html", map[string]interface{}{"Body": string(body)})
}

func (app *App) HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// AdminLogoutHandler clears Basic Auth by always returning 401.
// The XHR in admin.html sends deliberately wrong credentials here first,
// which causes the browser to cache the wrong creds and effectively log out.
func (app *App) AdminLogoutHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Basic realm="F3 Denver Admin"`)
	w.WriteHeader(http.StatusUnauthorized)
}

func nullEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}

const defaultTipsMarkdown = `# Tips & Troubleshooting

Use this page for common fixes and self-service help. Admins can edit this content at **Admin → Edit Tips Page**.

---

## Preblasts & Backblasts

- **Missing or wrong date?** Submit a ticket with the correct AO and event date.
- **Wrong AO?** Include the correct AO and event date in your ticket.

## Yeti / Achievement Tracking

- **Post not counting?** Check [Yeti Tracker](https://yeti.f3denverco.com/) with the correct date range and region.
- **Yeti Beast badge:** You need 50+ posts, 10+ AOs, and 6+ Qs in the current season.

## F3 Denver Website

- **Broken link or wrong info?** Submit a ticket with the exact URL and a short description.
- **Suggestions?** Use the "Other" ticket type and describe your idea.

## Other

- For anything that doesn't fit above, choose **Other** and describe your request.

---

*F3 Denver — Leave no man behind.*
`

// AOsList is the list of active AOs for dropdown (derived from Yeti tracker).
var AOsList = []string{
	"ao_blackops", "ao_bond-fire", "ao_bluecifer", "ao_castle-pines", "ao_central-park",
	"ao_cpr", "ao_disruption", "ao_downrange", "ao_erie", "ao_erie_rucking", "ao_forge",
	"ao_gotham", "ao_lions-den", "ao_littleton", "ao_louisville", "ao_manchester",
	"ao_nomad", "ao_northfield", "ao_off-the-books", "ao_off-the-books_broomfield",
	"ao_outback-run-club", "ao_parker", "ao_red-rucks", "ao_the-grindstone", "ao_the-pit",
	"ao_trailhead-run-or-ruck", "ao_wash-park", "ao_downrange_cos_valhalla",
}

func (app *App) renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	t, err := template.ParseFiles(
		filepath.Join(app.templatesDir, "base.html"),
		filepath.Join(app.templatesDir, name),
	)
	if err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}
	if err := t.Execute(w, data); err != nil {
		log.Printf("template execute %s: %v", name, err)
	}
}
