# Flowwow Shop Statistics Scraper

A Go-based web scraper that tracks Flowwow shop performance (purchases, reviews, rating) using `chromedp` and stores historical data in SQLite. It includes automated Telegram reporting and scheduling.

## 🚀 Features

- **Data Tracking**: Monitors purchases and reviews over time.
- **Reporting**: Daily/Weekly/Monthly growth statistics sent to Telegram.
- **Optimized**: Blocks media/images for low memory footprint (ideal for small VPS).
- **Scheduled**: Built-in `systemd` timer for nightly runs.

## 🛠 Setup

1. **Environment**: Copy `.env.example` (or create `.env`) and fill in:
   - `TELEGRAM_BOT_TOKEN`
   - `TELEGRAM_CHAT_ID`
2. **Shops**: Add shop slugs to `shops.txt` (one per line).
3. **Run**: 
   ```bash
   go run main.go -mode scrape # Scrape and send report
   go run main.go -mode stats  # Only send stats from DB
   ```

## 📦 Deployment (AlmaLinux / RHEL)

For a server with limited RAM (e.g., 704MB):

1. **Install Browser**: `sudo dnf install -y chromium`
2. **Deploy**: Run `./deploy.sh` on your Mac to cross-compile and upload.
3. **Schedule**:
   ```bash
   sudo mv flowwow-stats.* /etc/systemd/system/
   sudo systemctl enable --now flowwow-stats.timer
   ```
