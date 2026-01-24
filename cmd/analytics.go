package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var analyticsCmd = &cobra.Command{
	Use:   "analytics",
	Short: "Manage prompt analytics",
	Long: `Manage prompt analytics for Frank.

Analytics captures prompts from your Claude Code sessions and uploads
them to S3 for analysis. The dashboard shows prompt patterns, skill
opportunities, and effectiveness metrics.

Examples:
  frank analytics status                    # Show analytics status
  frank analytics list                      # List recent prompts
  frank analytics sync                      # Sync local analytics to S3
  frank analytics report                    # Generate local report`,
}

// Flags
var (
	analyticsBucket string
	analyticsRegion string
	analyticsDays   int
	analyticsFormat string
)

func init() {
	rootCmd.AddCommand(analyticsCmd)

	// Add subcommands
	analyticsCmd.AddCommand(analyticsStatusCmd)
	analyticsCmd.AddCommand(analyticsListCmd)
	analyticsCmd.AddCommand(analyticsSyncCmd)
	analyticsCmd.AddCommand(analyticsReportCmd)

	// Common flags
	analyticsCmd.PersistentFlags().StringVar(&analyticsBucket, "bucket", "", "S3 bucket name (default: from AWS_ANALYTICS_BUCKET)")
	analyticsCmd.PersistentFlags().StringVar(&analyticsRegion, "region", "us-east-1", "AWS region")
	analyticsListCmd.Flags().IntVar(&analyticsDays, "days", 7, "Number of days to list")
	analyticsReportCmd.Flags().StringVar(&analyticsFormat, "format", "html", "Output format (html, json)")
}

// ============================================================================
// analytics status - Show analytics configuration and status
// ============================================================================

var analyticsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show analytics status",
	Long:  `Show the current analytics configuration and collection status.`,
	RunE:  runAnalyticsStatus,
}

func runAnalyticsStatus(cmd *cobra.Command, args []string) error {
	green := color.New(color.FgGreen).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()

	fmt.Println("Frank Analytics Status")
	fmt.Println(strings.Repeat("=", 40))

	// Check S3 bucket
	bucket := getBucket()
	if bucket == "" {
		fmt.Printf("S3 Bucket:     %s\n", red("Not configured"))
		fmt.Println("\nTo enable analytics, set ANALYTICS_BUCKET environment variable")
		fmt.Println("or use --bucket flag.")
	} else {
		fmt.Printf("S3 Bucket:     %s\n", green(bucket))
	}

	fmt.Printf("AWS Region:    %s\n", analyticsRegion)

	// Check local analytics directory
	localDir := getLocalAnalyticsDir()
	if _, err := os.Stat(localDir); os.IsNotExist(err) {
		fmt.Printf("Local Storage: %s (not created)\n", yellow(localDir))
	} else {
		// Count files
		count := 0
		filepath.Walk(localDir, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(path, ".json") {
				count++
			}
			return nil
		})
		fmt.Printf("Local Storage: %s (%d files)\n", green(localDir), count)
	}

	// Check if S3 is accessible
	if bucket != "" {
		ctx := context.Background()
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(analyticsRegion))
		if err == nil {
			client := s3.NewFromConfig(cfg)
			_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
				Bucket: aws.String(bucket),
			})
			if err == nil {
				fmt.Printf("S3 Access:     %s\n", green("Connected"))
			} else {
				fmt.Printf("S3 Access:     %s (%v)\n", red("Error"), err)
			}
		}
	}

	fmt.Println()
	fmt.Println("Dashboard URL: https://frank.digitaldevops.io/dashboard")

	return nil
}

// ============================================================================
// analytics list - List recent prompts
// ============================================================================

var analyticsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent prompts",
	Long:  `List prompts captured in the analytics system.`,
	RunE:  runAnalyticsList,
}

func runAnalyticsList(cmd *cobra.Command, args []string) error {
	bucket := getBucket()
	if bucket == "" {
		return fmt.Errorf("S3 bucket not configured. Set ANALYTICS_BUCKET or use --bucket flag")
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(analyticsRegion))
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)

	// Calculate date range
	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -analyticsDays)

	// List prompts from S3
	prefix := "prompts/"

	result, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(100),
	})
	if err != nil {
		return fmt.Errorf("failed to list S3 objects: %w", err)
	}

	if len(result.Contents) == 0 {
		fmt.Println("No prompts found in analytics bucket.")
		fmt.Println("\nPrompts will appear here after using Frank with analytics enabled.")
		return nil
	}

	// Parse and display prompts
	type PromptSummary struct {
		Time    time.Time
		Profile string
		Text    string
		Turns   int
	}

	var summaries []PromptSummary

	for _, obj := range result.Contents {
		if obj.LastModified != nil && obj.LastModified.After(startDate) {
			// Extract profile from key
			parts := strings.Split(*obj.Key, "/")
			profile := "unknown"
			if len(parts) >= 2 {
				profile = parts[1]
			}

			// Fetch and parse
			getResult, err := client.GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(bucket),
				Key:    obj.Key,
			})
			if err != nil {
				continue
			}
			defer getResult.Body.Close()

			var prompts []map[string]interface{}
			decoder := json.NewDecoder(getResult.Body)
			if err := decoder.Decode(&prompts); err != nil {
				continue
			}

			for _, p := range prompts {
				promptData, ok := p["prompt"].(map[string]interface{})
				if !ok {
					continue
				}

				text, _ := promptData["text"].(string)
				if len(text) > 60 {
					text = text[:57] + "..."
				}

				timestamp, _ := p["timestamp"].(string)
				t, _ := time.Parse(time.RFC3339, timestamp)

				outcomeData, _ := p["outcome"].(map[string]interface{})
				turns, _ := outcomeData["next_turn_count"].(float64)

				summaries = append(summaries, PromptSummary{
					Time:    t,
					Profile: profile,
					Text:    text,
					Turns:   int(turns),
				})
			}
		}
	}

	if len(summaries) == 0 {
		fmt.Printf("No prompts found in the last %d days.\n", analyticsDays)
		return nil
	}

	// Sort by time descending
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Time.After(summaries[j].Time)
	})

	// Display as table
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"TIME", "PROFILE", "PROMPT", "TURNS"})
	table.SetBorder(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator("  ")
	table.SetRowSeparator("")
	table.SetColWidth(60)

	for _, s := range summaries[:min(20, len(summaries))] {
		table.Append([]string{
			s.Time.Format("Jan 02 15:04"),
			s.Profile,
			s.Text,
			fmt.Sprintf("%d", s.Turns),
		})
	}

	table.Render()

	if len(summaries) > 20 {
		fmt.Printf("\nShowing 20 of %d prompts. View all at: https://frank.digitaldevops.io/dashboard\n", len(summaries))
	}

	return nil
}

// ============================================================================
// analytics sync - Sync local analytics to S3
// ============================================================================

var analyticsSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync local analytics to S3",
	Long:  `Upload locally captured analytics to S3 for dashboard viewing.`,
	RunE:  runAnalyticsSync,
}

func runAnalyticsSync(cmd *cobra.Command, args []string) error {
	bucket := getBucket()
	if bucket == "" {
		return fmt.Errorf("S3 bucket not configured. Set ANALYTICS_BUCKET or use --bucket flag")
	}

	localDir := getLocalAnalyticsDir()
	if _, err := os.Stat(localDir); os.IsNotExist(err) {
		fmt.Println("No local analytics to sync.")
		return nil
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(analyticsRegion))
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)

	// Walk local directory and upload files
	uploaded := 0
	err = filepath.Walk(localDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		// Read file
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Determine S3 key
		relPath, _ := filepath.Rel(localDir, path)
		key := "prompts/local/" + strings.ReplaceAll(relPath, "\\", "/")

		// Upload
		_, err = client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(bucket),
			Key:         aws.String(key),
			Body:        strings.NewReader(string(data)),
			ContentType: aws.String("application/json"),
		})
		if err != nil {
			fmt.Printf("Failed to upload %s: %v\n", path, err)
			return nil
		}

		uploaded++
		return nil
	})

	if err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}

	if uploaded == 0 {
		fmt.Println("No files to sync.")
	} else {
		fmt.Printf("Synced %d files to s3://%s/prompts/local/\n", uploaded, bucket)
	}

	return nil
}

// ============================================================================
// analytics report - Generate local HTML report
// ============================================================================

var analyticsReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate analytics report",
	Long:  `Generate a local HTML or JSON report of analytics data.`,
	RunE:  runAnalyticsReport,
}

func runAnalyticsReport(cmd *cobra.Command, args []string) error {
	bucket := getBucket()
	if bucket == "" {
		return fmt.Errorf("S3 bucket not configured. Set ANALYTICS_BUCKET or use --bucket flag")
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(analyticsRegion))
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)

	// Fetch aggregates
	result, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucket),
		Prefix:  aws.String("aggregates/daily/"),
		MaxKeys: aws.Int32(30),
	})
	if err != nil {
		return fmt.Errorf("failed to list aggregates: %w", err)
	}

	if len(result.Contents) == 0 {
		fmt.Println("No aggregated data available yet.")
		fmt.Println("Aggregation runs daily at 2 AM UTC.")
		return nil
	}

	// Collect aggregate data
	var aggregates []map[string]interface{}

	for _, obj := range result.Contents {
		getResult, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    obj.Key,
		})
		if err != nil {
			continue
		}
		defer getResult.Body.Close()

		var agg map[string]interface{}
		decoder := json.NewDecoder(getResult.Body)
		if err := decoder.Decode(&agg); err != nil {
			continue
		}
		aggregates = append(aggregates, agg)
	}

	// Output based on format
	if analyticsFormat == "json" {
		data, _ := json.MarshalIndent(aggregates, "", "  ")
		fmt.Println(string(data))
	} else {
		// Generate HTML
		outputPath := filepath.Join(os.TempDir(), "frank-analytics-report.html")
		html := generateReportHTML(aggregates)
		if err := os.WriteFile(outputPath, []byte(html), 0644); err != nil {
			return fmt.Errorf("failed to write report: %w", err)
		}
		fmt.Printf("Report generated: %s\n", outputPath)
		fmt.Println("Open in browser to view.")
	}

	return nil
}

// Helper functions

func getBucket() string {
	if analyticsBucket != "" {
		return analyticsBucket
	}
	return os.Getenv("ANALYTICS_BUCKET")
}

func getLocalAnalyticsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".frank", "analytics")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func generateReportHTML(aggregates []map[string]interface{}) string {
	// Calculate totals
	var totalPrompts, totalCost float64
	var positiveFeedback, negativeFeedback int

	for _, agg := range aggregates {
		if metrics, ok := agg["metrics"].(map[string]interface{}); ok {
			if v, ok := metrics["total_prompts"].(float64); ok {
				totalPrompts += v
			}
			if v, ok := metrics["total_cost_usd"].(float64); ok {
				totalCost += v
			}
			if v, ok := metrics["feedback_positive"].(float64); ok {
				positiveFeedback += int(v)
			}
			if v, ok := metrics["feedback_negative"].(float64); ok {
				negativeFeedback += int(v)
			}
		}
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <title>Frank Analytics Report</title>
    <style>
        body { font-family: -apple-system, sans-serif; background: #1e1e1e; color: #d4d4d4; padding: 40px; }
        h1 { color: #4ec9b0; }
        .metrics { display: flex; gap: 20px; margin: 20px 0; }
        .metric { background: #252526; padding: 20px; border-radius: 8px; }
        .metric-value { font-size: 32px; color: #4ec9b0; }
        .metric-label { font-size: 12px; color: #9d9d9d; text-transform: uppercase; }
    </style>
</head>
<body>
    <h1>Frank Analytics Report</h1>
    <p>Generated: %s</p>
    <div class="metrics">
        <div class="metric">
            <div class="metric-value">%.0f</div>
            <div class="metric-label">Total Prompts</div>
        </div>
        <div class="metric">
            <div class="metric-value">$%.2f</div>
            <div class="metric-label">Total Cost</div>
        </div>
        <div class="metric">
            <div class="metric-value">%d</div>
            <div class="metric-label">Positive Feedback</div>
        </div>
        <div class="metric">
            <div class="metric-value">%d</div>
            <div class="metric-label">Negative Feedback</div>
        </div>
    </div>
    <p>For detailed analytics, visit: <a href="https://frank.digitaldevops.io/dashboard" style="color: #4ec9b0;">Dashboard</a></p>
</body>
</html>`, time.Now().Format(time.RFC1123), totalPrompts, totalCost, positiveFeedback, negativeFeedback)
}
