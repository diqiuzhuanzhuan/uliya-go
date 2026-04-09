package main

import "testing"

func TestAnalyzeIntakeMessageAcceptsMetadataDrivenOrganizationIntents(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "by_extension",
			message: "按扩展名分类，把图片、视频、压缩包和文档分开。",
		},
		{
			name:    "by_modified_time",
			message: "按修改时间整理，按年/月建目录归档。",
		},
		{
			name:    "by_file_size",
			message: "按文件大小分类，小于 10MB 的放一类，10MB 到 100MB 的放一类，超过 100MB 的单独放。",
		},
		{
			name:    "excel_this_year_by_size",
			message: "把今年的 excel 文件按照大小来进行分类。",
		},
		{
			name:    "pdf_this_month_by_time",
			message: "只整理这个月新增的 PDF，按日期归档到每天的目录里。",
		},
		{
			name:    "documents_then_extension",
			message: "先筛出文档类文件，再按扩展名细分成 Word、Excel、PPT、PDF。",
		},
		{
			name:    "images_by_year_and_month",
			message: "照片和截图按拍摄或修改时间整理，优先按年份，再按月份分类。",
		},
		{
			name:    "videos_by_size",
			message: "视频文件按大小分层，大文件单独放到 big-videos 目录。",
		},
		{
			name:    "source_archives_by_extension_and_size",
			message: "把源码压缩包按扩展名和大小一起分类，zip、tar.gz、7z 分开后再区分大小。",
		},
		{
			name:    "recent_spreadsheets_by_size",
			message: "最近 30 天的表格文件按大小整理，超过 50MB 的单独归类。",
		},
		{
			name:    "english_extension_case",
			message: "Group files by extension, and split spreadsheets into Excel and CSV.",
		},
		{
			name:    "english_time_and_size_case",
			message: "Organize this year's Excel files by size, and archive older spreadsheets by month.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := analyzeIntakeMessage(nil, nil, tt.message, "/Users/ugreen/Downloads", "", "intent")
			if !got.Relevant {
				t.Fatalf("expected message to be relevant, got %#v", got)
			}
			if got.Intent != tt.message {
				t.Fatalf("expected intent %q, got %q", tt.message, got.Intent)
			}
			if got.Path != "" {
				t.Fatalf("expected no new path extraction, got %q", got.Path)
			}
			if got.UseCurrentWorkspace {
				t.Fatalf("did not expect current workspace inference for %q", tt.message)
			}
		})
	}
}
