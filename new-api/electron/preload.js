const { contextBridge } = require('electron');

// 获取数据目录路径（用于显示给用户）
// 优先使用主进程设置的真实路径，如果没有则回退到手动拼接
function getDataDirPath() {
  // 如果主进程已设置真实路径，直接使用
  if (process.env.ELECTRON_DATA_DIR) {
    return process.env.ELECTRON_DATA_DIR;
  }
}

contextBridge.exposeInMainWorld('electron', {
  isElectron: true,
  version: process.versions.electron,
  platform: process.platform,
  versions: process.versions,
  dataDir: getDataDirPath()
});