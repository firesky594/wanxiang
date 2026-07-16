<template>
  <section class="console mr-console">
    <header class="topbar">
      <RouterLink class="brand" to="/dashboard">
        <span class="brand-mark"><el-icon><Cpu /></el-icon></span>
        <span>Wanxiang Agent</span>
      </RouterLink>
      <nav class="nav" aria-label="主导航">
        <RouterLink to="/dashboard"><el-icon><ArrowRight /></el-icon>调度台</RouterLink>
        <RouterLink to="/issues"><el-icon><Warning /></el-icon>Issue</RouterLink>
        <RouterLink to="/deliveries"><el-icon><ArrowRight /></el-icon>交付验收</RouterLink>
      </nav>
    </header>

    <main class="main">
      <div class="page-head">
        <div>
          <p class="eyebrow">PROJECT INTEGRATION</p>
          <h1>审核轨道</h1>
          <p>查看 Agent 完成报告、测试证据与负责人审核记录。代码操作只由 Agent 接口执行。</p>
        </div>
        <el-button :loading="loading" plain @click="loadMergeRequests">
          <el-icon><Refresh /></el-icon>刷新
        </el-button>
      </div>

      <div class="summary-strip" aria-label="合并请求摘要">
        <div><strong>{{ items.length }}</strong><span>全部 MR</span></div>
        <div><strong>{{ pendingCount }}</strong><span>待负责人处理</span></div>
        <div><strong>{{ mergedCount }}</strong><span>已合并</span></div>
      </div>

      <el-alert v-if="error" class="error-alert" type="error" :closable="false" title="加载失败" :description="error" />

      <section class="review-layout" aria-live="polite" :aria-busy="loading">
        <aside class="mr-queue panel" aria-label="合并请求列表">
          <div class="section-title">
            <div><span class="rail-mark"></span><h2>MR 队列</h2></div>
            <span class="muted">{{ items.length }} 条</span>
          </div>
          <div v-if="loading && !items.length" class="empty-state">正在读取审核记录…</div>
          <div v-else-if="!items.length" class="empty-state">
            <strong>暂无合并请求</strong>
            <span>执行 Agent 提交完成报告后会出现在这里。</span>
          </div>
          <button
            v-for="item in items"
            :key="item.merge_request.id"
            class="mr-card"
            :class="{ active: selected?.merge_request.id === item.merge_request.id }"
            type="button"
            @click="selectMR(item.merge_request.id)"
          >
            <span class="mr-card-top">
              <span class="mono">MR-{{ item.merge_request.id }}</span>
              <span class="status-chip" :data-status="item.merge_request.status">{{ statusLabel(item.merge_request.status) }}</span>
            </span>
            <strong>{{ item.merge_request.title }}</strong>
            <span class="branch mono">{{ item.merge_request.source_branch }} → main</span>
            <span class="owner">负责人 {{ item.merge_request.project_lead || '未登记' }}</span>
          </button>
        </aside>

        <article v-if="selected" class="evidence panel">
          <header class="evidence-head">
            <div>
              <span class="mono muted">MR-{{ selected.merge_request.id }} / STEP-{{ selected.merge_request.step_id }}</span>
              <h2>{{ selected.merge_request.title }}</h2>
            </div>
            <span class="status-chip large" :data-status="selected.merge_request.status">{{ statusLabel(selected.merge_request.status) }}</span>
          </header>

          <div class="report-meta">
            <div><span>报告版本</span><strong>v{{ selected.report.version }}</strong></div>
            <div><span>执行 Agent</span><strong>{{ selected.report.agent_name }}</strong></div>
            <div><span>项目负责人</span><strong>{{ selected.merge_request.project_lead }}</strong></div>
            <div><span>HEAD</span><strong class="mono commit">{{ shortCommit(selected.report.head_commit) }}</strong></div>
          </div>

          <section class="evidence-section">
            <h3><span class="step-index">01</span>完成事项</h3>
            <ul v-if="selected.report.completed.length" class="proof-list success-list">
              <li v-for="entry in selected.report.completed" :key="entry">{{ entry }}</li>
            </ul>
            <p v-else class="muted">没有登记完成事项。</p>
          </section>

          <section class="evidence-section">
            <h3><span class="step-index">02</span>测试证据</h3>
            <div v-if="selected.report.tests.length" class="test-grid">
              <div v-for="test in selected.report.tests" :key="test.command" class="test-proof">
                <span class="test-status" :data-status="test.status">{{ test.status }}</span>
                <code>{{ test.command }}</code>
                <p v-if="test.summary">{{ test.summary }}</p>
              </div>
            </div>
            <p v-else class="muted">没有登记测试证据。</p>
          </section>

          <section class="evidence-section risk-section">
            <h3><span class="step-index">03</span>风险与未完成项</h3>
            <div class="risk-grid">
              <div><span>风险</span><p v-for="risk in selected.report.risks" :key="risk">{{ risk }}</p><p v-if="!selected.report.risks.length" class="muted">无已知风险</p></div>
              <div><span>未完成</span><p v-for="entry in selected.report.incomplete" :key="entry">{{ entry }}</p><p v-if="!selected.report.incomplete.length" class="muted">无未完成项</p></div>
            </div>
          </section>

          <section class="evidence-section review-track">
            <h3><span class="step-index">04</span>审核记录</h3>
            <div v-if="selected.reviews.length" class="timeline">
              <div v-for="review in selected.reviews" :key="review.id" class="timeline-item">
                <span class="timeline-dot"></span>
                <div><strong>{{ review.reviewer }}</strong><span class="muted">{{ statusLabel(review.status) }}</span><p>{{ review.body || '审核通过，未附加意见。' }}</p></div>
              </div>
            </div>
            <p v-else class="muted">等待项目负责人审核。</p>
          </section>
        </article>

        <div v-else-if="items.length" class="panel empty-state detail-empty">选择一条 MR 查看完整证据。</div>
      </section>
    </main>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ArrowRight, Cpu, Refresh, Warning } from '@element-plus/icons-vue'
import { getMergeRequest, listMergeRequests, type MergeRequestDetail } from '../api/client'

const items = ref<MergeRequestDetail[]>([])
const selected = ref<MergeRequestDetail | null>(null)
const loading = ref(false)
const error = ref('')
const pendingCount = computed(() => items.value.filter((item) => ['pending_review', 'changes_requested', 'approved'].includes(item.merge_request.status)).length)
const mergedCount = computed(() => items.value.filter((item) => item.merge_request.status === 'merged').length)

const labels: Record<string, string> = { pending_review: '待审核', changes_requested: '需修改', approved: '已批准', merged: '已合并', closed: '已关闭', passed: '通过' }
const statusLabel = (status: string) => labels[status] || status
const shortCommit = (commit: string) => commit ? commit.slice(0, 10) : '—'

async function loadMergeRequests() {
  loading.value = true
  error.value = ''
  try {
    items.value = await listMergeRequests()
    if (items.value.length) await selectMR(selected.value?.merge_request.id || items.value[0].merge_request.id)
    else selected.value = null
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '无法读取合并请求'
  } finally {
    loading.value = false
  }
}

async function selectMR(id: number) {
  try {
    selected.value = await getMergeRequest(id)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '无法读取合并请求详情'
  }
}

onMounted(loadMergeRequests)
</script>

<style scoped>
.mr-console { --rail: #2f7d68; --rail-soft: #e6f0eb; }
.eyebrow { margin: 0 0 7px !important; color: var(--rail) !important; font: 700 11px/1.2 "SFMono-Regular", Consolas, monospace; letter-spacing: .16em; }
.summary-strip { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); margin-bottom: 16px; border: 1px solid var(--wx-line); background: #fff; }
.summary-strip div { display: flex; align-items: baseline; gap: 10px; padding: 14px 18px; border-right: 1px solid var(--wx-line); }
.summary-strip div:last-child { border-right: 0; }
.summary-strip strong { font-size: 23px; }
.summary-strip span { color: var(--wx-muted); font-size: 13px; }
.error-alert { margin-bottom: 16px; }
.review-layout { display: grid; grid-template-columns: minmax(280px, 360px) minmax(0, 1fr); gap: 16px; align-items: start; }
.mr-queue { padding: 0; overflow: hidden; position: sticky; top: 82px; }
.section-title { display: flex; align-items: center; justify-content: space-between; padding: 16px 18px; border-bottom: 1px solid var(--wx-line); }
.section-title div { display: flex; align-items: center; gap: 9px; }
.section-title h2 { margin: 0; font-size: 16px; }
.rail-mark { width: 4px; height: 18px; background: var(--rail); }
.mr-card { width: 100%; display: grid; gap: 8px; padding: 16px 18px; text-align: left; color: inherit; background: #fff; border: 0; border-bottom: 1px solid var(--wx-line); cursor: pointer; transition: background .18s ease, box-shadow .18s ease; }
.mr-card:hover, .mr-card:focus-visible { background: #f7faf8; outline: none; box-shadow: inset 4px 0 var(--rail); }
.mr-card.active { background: var(--rail-soft); box-shadow: inset 4px 0 var(--rail); }
.mr-card-top, .evidence-head { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; }
.branch, .owner { color: var(--wx-muted); font-size: 12px; overflow-wrap: anywhere; }
.status-chip { display: inline-flex; align-items: center; width: fit-content; padding: 4px 8px; border-radius: 999px; background: #eef1ee; color: #53605c; font-size: 11px; font-weight: 750; }
.status-chip[data-status="approved"], .status-chip[data-status="merged"] { background: #dff0e8; color: #256957; }
.status-chip[data-status="changes_requested"] { background: #fbe8df; color: #9b442f; }
.status-chip.large { padding: 7px 12px; font-size: 12px; }
.evidence { padding: 24px; }
.evidence-head { padding-bottom: 18px; border-bottom: 1px solid var(--wx-line); }
.evidence-head h2 { margin: 5px 0 0; }
.report-meta { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); border-bottom: 1px solid var(--wx-line); }
.report-meta div { display: grid; gap: 5px; padding: 15px 12px; border-right: 1px solid var(--wx-line); }
.report-meta div:last-child { border-right: 0; }
.report-meta span { color: var(--wx-muted); font-size: 11px; }
.commit { overflow: hidden; text-overflow: ellipsis; }
.evidence-section { padding: 20px 0; border-bottom: 1px solid var(--wx-line); }
.evidence-section:last-child { border-bottom: 0; padding-bottom: 0; }
.evidence-section h3 { display: flex; gap: 10px; align-items: center; margin: 0 0 14px; font-size: 15px; }
.step-index { color: var(--rail); font: 700 11px/1 "SFMono-Regular", Consolas, monospace; }
.proof-list { display: grid; gap: 8px; margin: 0; padding: 0; list-style: none; }
.proof-list li { padding: 10px 12px 10px 34px; background: #f7faf8; position: relative; }
.success-list li::before { content: "✓"; position: absolute; left: 12px; color: var(--rail); font-weight: 800; }
.test-grid { display: grid; gap: 9px; }
.test-proof { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 10px; align-items: center; padding: 11px 12px; background: #18221f; color: #eef8f3; border-radius: 6px; }
.test-proof code { overflow-wrap: anywhere; }
.test-proof p { grid-column: 2; margin: 0; color: #b9c8c1; }
.test-status { color: #72d4ae; font-size: 11px; text-transform: uppercase; }
.risk-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.risk-grid > div { padding: 13px; border: 1px solid var(--wx-line); background: #fbfcfa; }
.risk-grid span { color: var(--wx-amber); font-size: 11px; font-weight: 800; }
.risk-grid p { margin: 8px 0 0; }
.timeline { display: grid; }
.timeline-item { display: grid; grid-template-columns: 16px 1fr; gap: 9px; padding-bottom: 15px; }
.timeline-dot { width: 9px; height: 9px; margin-top: 5px; border-radius: 50%; background: var(--rail); box-shadow: 0 0 0 4px var(--rail-soft); }
.timeline-item div { display: grid; grid-template-columns: auto 1fr; gap: 8px; }
.timeline-item p { grid-column: 1 / -1; margin: 3px 0 0; }
.empty-state { display: grid; place-items: center; gap: 7px; min-height: 180px; padding: 28px; color: var(--wx-muted); text-align: center; }
.detail-empty { min-height: 420px; }
@media (max-width: 880px) { .review-layout { grid-template-columns: 1fr; } .mr-queue { position: static; } .report-meta { grid-template-columns: 1fr 1fr; } .summary-strip { grid-template-columns: 1fr; } .summary-strip div { border-right: 0; border-bottom: 1px solid var(--wx-line); } .risk-grid { grid-template-columns: 1fr; } }
@media (prefers-reduced-motion: reduce) { .mr-card { transition: none; } }
</style>
