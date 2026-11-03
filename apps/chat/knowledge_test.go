package main

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeImportPublishSearchAndDisable(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过知识库 worker 集成测试")
	}
	root := t.TempDir()
	script, err := filepath.Abs("../../tools/knowledge/server.cjs")
	if err != nil {
		t.Fatal(err)
	}
	service, err := newKnowledgeService(filepath.Join(t.TempDir(), "config.yaml"), KnowledgeConfig{
		Enabled: true,
		Root:    root,
		Node:    "node",
		Script:  script,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := service.importText(knowledgeImportInput{
		Files: []knowledgeUploadFile{{
			FileName: "家庭笔记.md",
			Content:  []byte("# 家庭笔记\n\n## 路由器\n\n重启路由器前，先确认电源和网线已经插好。\n"),
		}, {
			FileName: "应急信息.txt",
			Content:  []byte("停电时先关闭大功率电器，再检查配电箱。\n"),
		}},
		Name:        "家庭笔记",
		Author:      "家庭共同整理",
		Description: "家里的网络排障笔记",
		Topics:      []string{"网络", "居家"},
		UseCases:    []string{"询问路由器重启步骤时"},
		Avoid:       []string{"不要猜测运营商故障原因"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "completed" || job.SourceID == "" || job.VersionID == "" {
		t.Fatalf("导入任务状态不正确: %+v", job)
	}
	if job.Validation.SavedFiles != 2 || job.Validation.IndexedFiles != 2 {
		t.Fatalf("多文件校验统计不正确: %+v", job.Validation)
	}

	before, err := service.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source := findKnowledgeSource(t, before, job.SourceID)
	if source.PublishedVersion != "" {
		t.Fatalf("导入后应处于待发布状态: %+v", source)
	}
	if source.Coverage.SavedFiles != 2 || source.Coverage.IndexedFiles != 2 {
		t.Fatalf("待发布文件覆盖不正确: %+v", source.Coverage)
	}

	if _, err := service.publish(job.SourceID, before.Generation, job.VersionID); err != nil {
		t.Fatal(err)
	}
	afterPublish, err := service.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source = findKnowledgeSource(t, afterPublish, job.SourceID)
	if source.PublishedVersion != job.VersionID || !source.Enabled {
		t.Fatalf("发布后版本状态不正确: %+v", source)
	}

	raw, err := service.search(context.Background(), "路由器重启步骤", job.SourceID, 3)
	if err != nil {
		t.Fatal(err)
	}
	var search knowledgeSearchFixture
	if err := json.Unmarshal(raw, &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Matches) == 0 {
		t.Fatal("发布后的资料检索不到")
	}
	match := search.Matches[0]
	if !strings.HasPrefix(match.SectionID, job.SourceID+"@"+job.VersionID+"/") {
		t.Fatalf("片段 ID 没有包含资料与版本: %s", match.SectionID)
	}
	readRaw, err := service.readSection(context.Background(), match.SectionID, 0, 3000)
	if err != nil {
		t.Fatal(err)
	}
	var read knowledgeReadFixture
	if err := json.Unmarshal(readRaw, &read); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read.Text, "先确认电源和网线") {
		t.Fatalf("读取到的原文不正确: %q", read.Text)
	}

	disabled := false
	if _, err := service.patch(job.SourceID, afterPublish.Generation, knowledgePatchRequest{
		ExpectedGeneration: afterPublish.Generation,
		Enabled:            &disabled,
	}); err != nil {
		t.Fatal(err)
	}
	afterDisable, err := service.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source = findKnowledgeSource(t, afterDisable, job.SourceID)
	if source.Enabled {
		t.Fatal("停用没有持久化")
	}
	allRaw, err := service.search(context.Background(), "路由器", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(allRaw, &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Matches) != 0 {
		t.Fatalf("停用资料仍然可检索: %+v", search.Matches)
	}
	if _, err := service.readSection(context.Background(), match.SectionID, 0, 3000); err == nil {
		t.Fatal("停用资料仍然可读取")
	}
	if _, err := service.patch(job.SourceID, afterPublish.Generation, knowledgePatchRequest{}); !errors.Is(err, errKnowledgeConflict) {
		t.Fatalf("旧 generation 应返回冲突，实际: %v", err)
	}

	restarted, err := newKnowledgeService(filepath.Join(t.TempDir(), "config.yaml"), KnowledgeConfig{
		Enabled: true,
		Root:    root,
		Node:    "node",
		Script:  script,
	})
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := restarted.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if findKnowledgeSource(t, afterRestart, job.SourceID).Enabled {
		t.Fatal("重启后停用状态丢失")
	}
}

type knowledgeSearchFixture struct {
	Matches []struct {
		SectionID string `json:"section_id"`
	} `json:"matches"`
}

type knowledgeReadFixture struct {
	Text string `json:"text"`
}

func findKnowledgeSource(t *testing.T, list knowledgeList, id string) knowledgeSourceView {
	t.Helper()
	for _, source := range list.Sources {
		if source.ID == id {
			return source
		}
	}
	t.Fatalf("没有找到知识库资料 %s", id)
	return knowledgeSourceView{}
}
