package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/skill"
)

type listSkillsTool struct{ pool *skill.Pool }

func (t listSkillsTool) Spec() llm.ToolSpec {
	return toolSpec("list_skills", "列出本机可用技能的名称与用途。遇到需要按既有流程操作软件（例如 BMA）时，先查看技能清单。", map[string]any{})
}

func (t listSkillsTool) Execute(_ context.Context, _ json.RawMessage, _ *ToolContext) (ToolResult, error) {
	if t.pool == nil {
		return ToolResult{Status: StatusOK, Data: map[string]any{"skills": []any{}}}, nil
	}
	items := t.pool.All()
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]string{"name": item.Name, "description": item.Description})
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{"skills": out}}, nil
}

type loadSkillTool struct{ pool *skill.Pool }

func (t loadSkillTool) Spec() llm.ToolSpec {
	return toolSpec("load_skill", "读取一个技能的完整说明（系统介绍、接口、部署与操作步骤）。名称来自 list_skills 或【可用技能】清单。",
		map[string]any{
			"name": map[string]any{"type": "string", "description": "技能名称，例如 bma"},
		}, "name")
}

func (t loadSkillTool) Execute(_ context.Context, args json.RawMessage, _ *ToolContext) (ToolResult, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeArgs(args, &input); err != nil {
		return systemError(err), nil
	}
	if t.pool == nil {
		return systemError(fmt.Errorf("技能系统未启用")), nil
	}
	item := t.pool.Find(input.Name)
	if item == nil {
		return systemError(fmt.Errorf("没有找到技能：%s", input.Name)), nil
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{
		"name": item.Name, "path": item.Path, "content": item.Content,
	}}, nil
}

// SkillTools 暴露技能清单与按需读取工具。
func SkillTools(pool *skill.Pool) []Tool {
	return []Tool{listSkillsTool{pool: pool}, loadSkillTool{pool: pool}}
}
