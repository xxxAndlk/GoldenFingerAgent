-- 片段的角色来源。抽取长期记忆时必须区分“机主自己说的”“联系人说的”
-- 和“系统提示”，否则会把别人的话记成机主自己的意愿。
ALTER TABLE mem_episode ADD COLUMN role TEXT NOT NULL DEFAULT '';
