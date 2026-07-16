<template><section class="console"><header class="topbar"><RouterLink class="brand" to="/dashboard">Wanxiang Agent</RouterLink><nav class="nav"><RouterLink to="/dashboard">调度台</RouterLink><RouterLink to="/deliveries">交付验收</RouterLink></nav></header><main class="main"><div class="page-head"><div><p class="eyebrow">LOCAL PIPELINE</p><h1>测试、发布与回滚</h1><p>测试和构建可自动执行；发布、迁移、删除与回滚必须单独确认。</p></div><el-button :loading="loading" @click="load">刷新</el-button></div><el-alert title="高风险确认边界" description="验收不代表发布授权。确认前请核对安全提交、产物哈希和回滚入口。" type="warning" :closable="false"/><section class="runs"><article v-for="run in runs" :key="run.ID" class="panel"><header><strong>RUN-{{run.ID}} · PROJECT-{{run.ProjectID}}</strong><el-tag>{{run.Status}}</el-tag></header><p class="mono">SAFE {{run.SafeCommit||'—'}} · ARTIFACT {{run.ArtifactHash||'—'}}</p><div v-for="step in run.Steps" :key="step.ID" class="step"><span>{{step.Kind}} / {{step.Key}}</span><b>{{step.Status}}</b><small>尝试 {{step.Attempt}} / {{step.MaxAttempts}} · {{step.FailureClass}}</small><code>{{step.Command}} {{step.Args.join(' ')}}</code><el-button v-if="step.Status==='awaiting_confirmation'" type="danger" plain @click="confirm(run.ID,step.Key)">确认高风险步骤</el-button></div></article><div v-if="!runs.length" class="panel empty">暂无流水线运行</div></section></main></section></template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { confirmPipeline, listPipelines, type PipelineRun } from '../api/client'
const runs = ref<PipelineRun[]>([])
const loading = ref(false)
async function load() { loading.value = true; try { runs.value = await listPipelines() } finally { loading.value = false } }
async function confirm(run: number, step: string) { await ElMessageBox.confirm('该操作可能修改本机生产状态，确认继续？', '高风险确认', { type: 'warning', confirmButtonText: '确认执行' }); await confirmPipeline(run, step); ElMessage.success('已记录单独确认'); await load() }
onMounted(load)
</script>
<style scoped>.eyebrow{color:#9a5d13;font:700 11px Consolas;letter-spacing:.16em}.runs{display:grid;gap:14px;margin-top:16px}.panel header{display:flex;justify-content:space-between}.step{display:grid;grid-template-columns:1fr auto;gap:8px;padding:14px 0;border-top:1px solid var(--wx-line)}.step small,.step code{grid-column:1/-1}.empty{text-align:center;color:var(--wx-muted)}@media(max-width:700px){.step{grid-template-columns:1fr}}</style>
