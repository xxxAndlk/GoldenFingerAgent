# Skill: {skill_name}

## 元信息

- **名称:** {skill_name}
- **中文名:** {display_name}
- **分类:** {category} (testing / debugging / collaboration / meta / domain)
- **境界要求:** {realm_requirement}
- **依赖工具:** {tools_required}

## 触发条件

```yaml
triggers:
  - task_type: {code | test | debug | plan | review | learn}
  - context_state: {error | multi_task | clean_slate | any}
  - domain_tags: [{tag1}, {tag2}]
  - min_realm: {realm_level}
```

## 行为描述

{这个技能做什么}

## 工作流

### Step 1: {步骤名}
{具体操作}

### Step 2: {步骤名}
{具体操作}

## System Prompt

```
{注入到 LLM system prompt 的内容}
```

## 检查点

- [ ] {检查项 1}
- [ ] {检查项 2}

## 输出

{技能产生的产出物}
