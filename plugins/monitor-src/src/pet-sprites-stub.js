/**
 * 桌面漫游宠物的空实现。
 *
 * 上游 pet-panel.js 在模块顶层 import 了 pet/sprites.js（页面级漫游宠物，
 * 由浏览器扩展的素材库供图）。桌面上没有这条通路，也不复刻这项功能：
 * 这里给出同形状的空实现，让 pet-panel 的 import 成立，运行时的
 * roamPetStart 一创建就得到空对象并立即回收。
 *
 * 不修改上游源码是移植的硬约束：以后同步上游只需替换 vendor/ 下的文件。
 */

export function create() {
  return {
    destroy() {}
  };
}
