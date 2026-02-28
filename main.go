package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"
)

const baseURL = "https://flowwow.com/shop/%s/?from=product"

type Stats struct {
	Slug      string
	Purchases int
	Reviews   int
	Rating    string
}

func main() {
	modeFlag := flag.String("mode", "scrape", "Mode to run: 'scrape' or 'stats'")
	flag.Parse()

	_ = godotenv.Load() // .env необязателен

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")

	if token == "" || chatID == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID not set")
	}

	db, err := sql.Open("sqlite", "flowwow.db")
	check(err)
	defer db.Close()

	initDB(db)

	slugs := loadShops("shops.txt")

	if *modeFlag == "stats" {
		runStats(db, slugs, token, chatID)
	} else {
		runScrape(db, slugs, token, chatID)
	}
}

func runStats(db *sql.DB, slugs []string, token, chatID string) {
	log.Println("Running statistics mode...")

	var report []string

	for _, slug := range slugs {
		// Get today's purchases
		var todayPurchases int
		err := db.QueryRow(`
			SELECT purchases FROM shop_records 
			JOIN shops ON shops.id = shop_records.shop_id 
			WHERE shops.slug = ? AND date(shop_records.created_at) = date('now')
			ORDER BY shop_records.created_at DESC LIMIT 1
		`, slug).Scan(&todayPurchases)

		if err == sql.ErrNoRows {
			log.Printf("No data for %s today. Skipping stats.", slug)
			continue
		} else if err != nil {
			log.Printf("DB error for %s (today): %v", slug, err)
			continue
		}

		yesterdayDiff := getDiff(db, slug, todayPurchases, "-1 day")
		weeklyDiff := getDiff(db, slug, todayPurchases, "-7 days")
		monthlyDiff := getDiff(db, slug, todayPurchases, "-30 days")

		shopReport := fmt.Sprintf(
			"📊 *%s*\n• Total: %d\n%s\n%s\n%s",
			slug,
			todayPurchases,
			formatDiffLine("Today", yesterdayDiff),
			formatDiffLine("Last 7 Days", weeklyDiff),
			formatDiffLine("Last 30 Days", monthlyDiff),
		)

		report = append(report, shopReport)
	}

	if len(report) > 0 {
		sendTelegram(token, chatID, strings.Join(report, "\n\n"))
		fmt.Println("Sent report:\n", strings.Join(report, "\n\n"))
	}
}

func getDiff(db *sql.DB, slug string, today int, interval string) *int {
	var pastPurchases int
	err := db.QueryRow(`
		SELECT purchases FROM shop_records 
		JOIN shops ON shops.id = shop_records.shop_id 
		WHERE shops.slug = ? AND date(shop_records.created_at) = date('now', ?)
		ORDER BY shop_records.created_at DESC LIMIT 1
	`, slug, interval).Scan(&pastPurchases)

	if err == sql.ErrNoRows {
		return nil // No data available for that literal date
	} else if err != nil {
		log.Printf("DB Error on interval %s for %s: %v", interval, slug, err)
		return nil
	}

	diff := today - pastPurchases
	return &diff
}

func formatDiffLine(label string, diff *int) string {
	if diff == nil {
		return fmt.Sprintf("• %s: No Data", label)
	}
	if *diff > 0 {
		return fmt.Sprintf("• %s: +%d 📈", label, *diff)
	} else if *diff < 0 {
		return fmt.Sprintf("• %s: %d 📉", label, *diff) // diff is already negative formatting
	}
	return fmt.Sprintf("• %s: 0 ➖", label)
}

func runScrape(db *sql.DB, slugs []string, token, chatID string) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-setuid-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()

	// Create a base browser context
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	// Ensure the browser is launched before creating tabs
	if err := chromedp.Run(browserCtx); err != nil {
		log.Fatal(err)
	}

	var report []string

	tx, err := db.Begin()
	check(err)
	defer tx.Rollback() // Rollback if not committed

	for _, slug := range slugs {
		shopURL := fmt.Sprintf(baseURL, slug)
		log.Println("Parsing:", shopURL)

		// Create a new tab context for each shop
		tabCtx, cancelTab := chromedp.NewContext(browserCtx)
		stats, err := parseShop(tabCtx, slug, shopURL)
		cancelTab() // Ensure tab is closed after the parse

		if err != nil {
			log.Println("error:", err)
			continue
		}

		err = saveStats(tx, stats)
		if err != nil {
			log.Printf("error saving stats for %s: %v", slug, err)
			continue
		}

		yesterdayDiff := getDiff(db, slug, stats.Purchases, "-1 day")
		weeklyDiff := getDiff(db, slug, stats.Purchases, "-7 days")
		monthlyDiff := getDiff(db, slug, stats.Purchases, "-30 days")

		report = append(report,
			fmt.Sprintf(
				"🏪 *%s*\n• Total: %d\n%s\n%s\n%s",
				stats.Slug,
				stats.Purchases,
				formatDiffLine("Today", yesterdayDiff),
				formatDiffLine("Last 7 Days", weeklyDiff),
				formatDiffLine("Last 30 Days", monthlyDiff),
			),
		)

		time.Sleep(5 * time.Second)
	}

	err = tx.Commit()
	if err != nil {
		log.Fatalf("Failed to commit transaction: %v", err)
	}

	if len(report) > 0 {
		sendTelegram(token, chatID, strings.Join(report, "\n\n"))
	}
}

func parseShop(ctx context.Context, slug, shopURL string) (Stats, error) {
	log.Printf("[DEBUG] shop=%s url=%s", slug, shopURL)

	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var res []string
	err := chromedp.Run(fetchCtx,
		network.Enable(),
		network.SetBlockedURLs([]string{"*.png", "*.jpg", "*.jpeg", "*.gif", "*.svg", "*.webp", "*/favicon.ico", "*.woff", "*.woff2", "*.ttf"}),
		network.SetExtraHTTPHeaders(network.Headers{
			"Accept-Language": "ru-RU,ru;q=0.9,en;q=0.8",
		}),
		chromedp.Navigate(shopURL),
		chromedp.Sleep(3*time.Second), // Allow time for bot challenge redirects and page rendering
		chromedp.Evaluate(`
			(function() {
				let rating = "0";
				let reviews = "0";
				let purchases = "0";
				
				let ratingNode = document.querySelector('.summary-rating-wrapper .top-line .rating .value');
				if (ratingNode) {
					rating = ratingNode.innerText.trim();
				} else {
					let ratingNode2 = document.querySelector('.rating.badge-detail .star-rating.backlight') || document.querySelector('.reviews-number');
					if (ratingNode2) rating = ratingNode2.innerText.trim();
				}

				let evals = document.querySelectorAll('.summary-rating-wrapper .top-line .evaluation');
				if (evals && evals.length >= 1) {
					let revNode = evals[0].querySelector('.text.backlight');
					if (revNode) reviews = revNode.innerText.trim();
				} else {
					let reviewsNode2 = document.querySelector('.rating.badge-detail .scores-number');
					if (reviewsNode2) reviews = reviewsNode2.innerText.trim();
				}
				
				if (evals && evals.length >= 2) {
					let purNode = evals[1].querySelector('.text.backlight');
					if (purNode) purchases = purNode.innerText.trim();
				}

				return [rating, reviews, purchases];
			})()
		`, &res),
	)
	if err != nil {
		log.Printf("[ERROR] Chromedp request failed: %v", err)
		return Stats{}, err
	}

	ratingStr := "0"
	reviewsStr := "0"
	purchasesStr := "0"
	if len(res) >= 3 {
		ratingStr = res[0]
		reviewsStr = res[1]
		purchasesStr = res[2]
	} else if len(res) >= 2 {
		ratingStr = res[0]
		reviewsStr = res[1]
	}

	stats := Stats{
		Slug:      slug,
		Rating:    ratingStr,
		Purchases: 0,
	}
	fmt.Sscanf(reviewsStr, "%d", &stats.Reviews)
	fmt.Sscanf(purchasesStr, "%d", &stats.Purchases)

	log.Printf("[DEBUG] parsed stats: %+v", stats)
	return stats, nil
}

func initDB(db *sql.DB) {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS shops (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT UNIQUE NOT NULL,
		added_at TEXT DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS shop_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		shop_id INTEGER,
		created_at TEXT,
		purchases INTEGER,
		reviews INTEGER,
		rating TEXT,
		FOREIGN KEY(shop_id) REFERENCES shops(id)
	);`)
	check(err)
}

func saveStats(tx *sql.Tx, s Stats) error {
	// Ensure shop exists
	_, err := tx.Exec(`INSERT OR IGNORE INTO shops (slug) VALUES (?)`, s.Slug)
	if err != nil {
		return err
	}

	// Fetch shop ID
	var shopID int
	err = tx.QueryRow(`SELECT id FROM shops WHERE slug = ?`, s.Slug).Scan(&shopID)
	if err != nil {
		return err
	}

	// Insert shop record
	_, err = tx.Exec(`
	INSERT INTO shop_records (shop_id, purchases, reviews, rating, created_at)
	VALUES (?, ?, ?, ?, datetime('now'))
	`, shopID, s.Purchases, s.Reviews, s.Rating)

	return err
}

func loadShops(file string) []string {
	data, err := os.ReadFile(file)
	check(err)

	lines := strings.Split(string(data), "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func sendTelegram(token, chatID, text string) {
	apiURL := fmt.Sprintf(
		"https://api.telegram.org/bot%s/sendMessage",
		token,
	)

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.PostForm(apiURL, url.Values{
		"chat_id":    {chatID},
		"text":       {text},
		"parse_mode": {"Markdown"},
	})
	if err != nil {
		log.Println("telegram error:", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("telegram error: status %d", resp.StatusCode)
	}
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
