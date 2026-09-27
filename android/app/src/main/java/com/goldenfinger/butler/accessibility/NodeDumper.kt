package com.goldenfinger.butler.accessibility

import android.graphics.Rect
import android.view.accessibility.AccessibilityNodeInfo
import org.json.JSONArray
import org.json.JSONObject

/**
 * NodeDumper：把窗口节点树递归转成 JSON（协议字段与 device-ws-contract 的
 * ScreenNode 逐字一致：text/content_description/class_name/bounds[4]/
 * clickable/scrollable/index），并维护 index → AccessibilityNodeInfo 缓存，
 * 供 Executor 按索引直接 performAction。
 */
class NodeDumper {
    // index → 节点缓存（每次 dump 重建；供 click/longClick/scroll 按索引操作）
    private val indexCache = HashMap<Int, AccessibilityNodeInfo>()

    /** 从根节点 dump 整棵窗口。返回 JSON 数组字符串。 */
    fun dump(root: AccessibilityNodeInfo?): JSONArray {
        indexCache.clear()
        val arr = JSONArray()
        var idx = 0
        walk(root, arr, idxRef = intArrayOf(0), idx)
        return arr
    }

    private fun walk(node: AccessibilityNodeInfo?, arr: JSONArray, idxRef: IntArray, _idx: Int) {
        if (node == null) return
        val bounds = Rect()
        node.getBoundsInScreen(bounds)
        val o = JSONObject()
        val index = idxRef[0]
        o.put("index", index)
        o.put("text", node.text?.toString() ?: "")
        o.put("content_description", node.contentDescription?.toString() ?: "")
        o.put("class_name", node.className?.toString() ?: "")
        o.put("bounds", JSONArray().put(bounds.left).put(bounds.top).put(bounds.right).put(bounds.bottom))
        o.put("clickable", node.isClickable)
        o.put("scrollable", node.isScrollable)
        // 缓存：仅保留可操作节点（点击/滚动），控制缓存体积
        if (node.isClickable || node.isScrollable || node.text != null) {
            indexCache[index] = node
        }
        arr.put(o)
        idxRef[0] = index + 1
        for (i in 0 until node.childCount) {
            val child = node.getChild(i)
            walk(child, arr, idxRef, index)
        }
    }

    /** 按 index 取缓存的节点（操作后失效需重新 dump）。 */
    fun nodeAt(index: Int): AccessibilityNodeInfo? = indexCache[index]

    fun clearCache() = indexCache.clear()
}
