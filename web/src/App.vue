<template>
  <el-config-provider>
    <RouterView v-if="route.meta.public" />
    <AdminShell v-else />
  </el-config-provider>
</template>

<script setup lang="ts">
import { RouterView, useRoute } from 'vue-router'
import AdminShell from './components/AdminShell.vue'

const route = useRoute()
</script>
