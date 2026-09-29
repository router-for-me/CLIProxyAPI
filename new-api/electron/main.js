const { app, BrowserWindow, dialog, Tray, Menu, shell } = require('electron');
const { spawn } = require('child_process');
const path = require('path');
const http = require('http');
const fs = require('fs');

let mainWindow;
let serverProcess;
let tray = null;
let serverErrorLogs = [];
const PORT = 3000;
const DEV_FRONTEND_PORT = 5173; // Rsbuild dev server port

// 保存日志到文件并打开
function saveAndOpenErrorLog() {
  try {
    const timestamp = new Date().toISOString().replace(/[:.]/g, '-');
    const logFileName = `new-api-crash-${timestamp}.log`;
    const logDir = app.getPath('logs');
    const logFilePath = path.join(logDir, logFileName);
    
    // 确保日志目录存在
    if (!fs.existsSync(logDir)) {
      fs.mkdirSync(logDir, { recursive: true });
    }
    
    // 写入日志
    const logContent = `New API 崩溃日志
生成时间: ${new Date().toLocaleString('zh-CN')}
平台: ${process.platform}
架构: ${process.arch}
应用版本: ${app.getVersion()}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

完整错误日志:

${serverErrorLogs.join('\n')}

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

日志文件位置: ${logFilePath}
`;
    
    fs.writeFileSync(logFilePath, logContent, 'utf8');
    
    // 打开日志文件
    shell.openPath(logFilePath).then((error) => {
      if (error) {
        console.error('Failed to open log file:', error);
        // 如果打开文件失败，至少显示文件位置
        shell.showItemInFolder(logFilePath);
      }
    });
    
    return logFilePath;
  } catch (err) {
    console.error('Failed to save error log:', err);
    return null;
  }
}

// 分析错误日志，识别常见错误并提供解决方案
function analyzeError(errorLogs) {
  const allLogs = errorLogs.join('\n');
  
  // 检测端口占用错误
  if (allLogs.includes('failed to start HTTP server') || 
      allLogs.includes('bind: address already in use') ||
      allLogs.includes('listen tcp') && allLogs.includes('bind: address already in use')) {
    return {
      type: '端口被占用',
      title: '端口 ' + PORT + ' 被占用',
      message: '无法启动服务器，端口已被其他程序占用',
      solution: `可能的解决方案：\n\n1. 关闭占用端口 ${PORT} 的其他程序\n2. 检查是否已经运行了另一个 New API 实例\n3. 使用以下命令查找占用端口的进程：\n   Mac/Linux: lsof -i :${PORT}\n   Windows: netstat -ano | findstr :${PORT}\n4. 重启电脑以释放端口`
    };
  }
  
  // 检测数据库错误
  if (allLogs.includes('database is locked') || 
      allLogs.includes('unable to open database')) {
    return {
      type: '数据文件被占用',
      title: '无法访问数据文件',
      message: '应用的数据文件正被其他程序占用',
      solution: '可能的解决方案：\n\n1. 检查是否已经打开了另一个 New API 窗口\n   - 查看任务栏/Dock 中是否有其他 New API 图标\n   - 查看系统托盘（Windows）或菜单栏（Mac）中是否有 New API 图标\n\n2. 如果刚刚关闭过应用，请等待 10 秒后再试\n\n3. 重启电脑以释放被占用的文件\n\n4. 如果问题持续，可以尝试：\n   - 退出所有 New API 实例\n   - 删除数据目录中的临时文件（.db-shm 和 .db-wal）\n   - 重新启动应用'
    };
  }
  
  // 检测权限错误
  if (allLogs.includes('permission denied') || 
      allLogs.includes('access denied')) {
    return {
      type: '权限错误',
      title: '权限不足',
      message: '程序没有足够的权限执行操作',
      solution: '可能的解决方案：\n\n1. 以管理员/root权限运行程序\n2. 检查数据目录的读写权限\n3. 检查可执行文件的权限\n4. 在 Mac 上，检查安全性与隐私设置'
    };
  }
  
  // 检测网络错误
  if (allLogs.includes('network is unreachable') || 
      allLogs.includes('no such host') ||
      allLogs.includes('connection refused')) {
    return {
      type: '网络错误',
      title: '网络连接失败',
      message: '无法建立网络连接',
      solution: '可能的解决方案：\n\n1. 检查网络连接是否正常\n2. 检查防火墙设置\n3. 检查代理配置\n4. 确认目标服务器地址正确'
    };
  }
  
  // 检测配置文件错误
  if (allLogs.includes('invalid configuration') || 
      allLogs.includes('failed to parse config') ||
      allLogs.includes('yaml') || allLogs.includes('json') && allLogs.includes('parse')) {
    return {
      type: '配置错误',
      title: '配置文件错误',
      message: '配置文件格式不正确或包含无效配置',
      solution: '可能的解决方案：\n\n1. 检查配置文件格式是否正确\n2. 恢复默认配置\n3. 删除配置文件让程序重新生成\n4. 查看文档了解正确的配置格式'
    };
  }
  
  // 检测内存不足
  if (allLogs.includes('out of memory') || 
      allLogs.includes('cannot allocate memory')) {
    return {
      type: '内存不足',
      title: '系统内存不足',
      message: '程序运行时内存不足',
      solution: '可能的解决方案：\n\n1. 关闭其他占用内存的程序\n2. 增加系统可用内存\n3. 重启电脑释放内存\n4. 检查是否存在内存泄漏'
    };
  }
  
  // 检测文件不存在错误
  if (allLogs.includes('no such file or directory') || 
      allLogs.includes('cannot find the file')) {
    return {
      type: '文件缺失',
      title: '找不到必需的文件',
      message: '缺少程序运行所需的文件',
      solution: '可能的解决方案：\n\n1. 重新安装应用程序\n2. 检查安装目录是否完整\n3. 确保所有依赖文件都存在\n4. 检查文件路径是否正确'
    };
  }
  
  return null;
}

function getBinaryPath() {
  const isDev = process.env.NODE_ENV === 'development';
  const platform = process.platform;

  if (isDev) {
    const binaryName = platform === 'win32' ? 'new-api.exe' : 'new-api';
    return path.join(__dirname, '..', binaryName);
  }

  let binaryName;
  switch (platform) {
    case 'win32':
      binaryName = 'new-api.exe';
      break;
    case 'darwin':
      binaryName = 'new-api';
      break;
    case 'linux':
      binaryName = 'new-api';
      break;
    default:
      binaryName = 'new-api';
  }

  return path.join(process.resourcesPath, 'bin', binaryName);
}

// Check if a server is available with retry logic
function checkServerAvailability(port, maxRetries = 30, retryDelay = 1000) {
  return new Promise((resolve, reject) => {
    let currentAttempt = 0;
    
    const tryConnect = () => {
      currentAttempt++;
      
      if (currentAttempt % 5 === 1 && currentAttempt > 1) {
        console.log(`Attempting to connect to port ${port}... (attempt ${currentAttempt}/${maxRetries})`);
      }
      
      const req = http.get({
        hostname: '127.0.0.1', // Use IPv4 explicitly instead of 'localhost' to avoid IPv6 issues
        port: port,
        timeout: 10000
      }, (res) => {
        // Server responded, connection successful
        req.destroy();
        console.log(`✓ Successfully connected to port ${port} (status: ${res.statusCode})`);
        resolve();
      });

      req.on('error', (err) => {
        if (currentAttempt >= maxRetries) {
          reject(new Error(`Failed to connect to port ${port} after ${maxRetries} attempts: ${err.message}`));
        } else {
          setTimeout(tryConnect, retryDelay);
        }
      });

      req.on('timeout', () => {
        req.destroy();
        if (currentAttempt >= maxRetries) {
          reject(new Error(`Connection timeout on port ${port} after ${maxRetries} attempts`));
        } else {
          setTimeout(tryConnect, retryDelay);
        }
      });
    };
    
    tryConnect();
  });
}

function startServer() {
  return new Promise((resolve, reject) => {
    const isDev = process.env.NODE_ENV === 'development';

    const userDataPath = app.getPath('userData');
    const dataDir = path.join(userDataPath, 'data');
    
    // 设置环境变量供 preload.js 使用
    process.env.ELECTRON_DATA_DIR = dataDir;
    
    if (isDev) {
      // 开发模式：假设开发者手动启动了 Go 后端和前端开发服务器
      // 只需要等待前端开发服务器就绪
      console.log('Development mode: skipping server startup');
      console.log('Please make sure you have started:');
      console.log('  1. Go backend: go run main.go (port 3000)');
      console.log('  2. Frontend dev server: make dev-web (port 5173)');
      console.log('');
      console.log('Checking if servers are running...');
      
      // First check if both servers are accessible
      checkServerAvailability(DEV_FRONTEND_PORT)
        .then(() => {
          console.log('✓ Frontend dev server is accessible on port 5173');
          resolve();
        })
        .catch((err) => {
          console.error(`✗ Cannot connect to frontend dev server on port ${DEV_FRONTEND_PORT}`);
          console.error('Please make sure the frontend dev server is running:');
          console.error('  make dev-web');
          reject(err);
        });
      return;
    }

    // 生产模式：启动二进制服务器
    const env = { ...process.env, PORT: PORT.toString() };

    if (!fs.existsSync(dataDir)) {
      fs.mkdirSync(dataDir, { recursive: true });
    }

    env.SQLITE_PATH = path.join(dataDir, 'new-api.db');
    
    console.log('━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━');
    console.log('📁 您的数据存储位置：');
    console.log('   ' + dataDir);
    console.log('   💡 备份提示：复制此目录即可备份所有数据');
    console.log('━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━');

    const binaryPath = getBinaryPath();
    const workingDir = process.resourcesPath;
    
    console.log('Starting server from:', binaryPath);

    serverProcess = spawn(binaryPath, [], {
      env,
      cwd: workingDir
    });

    serverProcess.stdout.on('data', (data) => {
      console.log(`Server: ${data}`);
    });

    serverProcess.stderr.on('data', (data) => {
      const errorMsg = data.toString();
      console.error(`Server Error: ${errorMsg}`);
      serverErrorLogs.push(errorMsg);
      // 只保留最近的100条错误日志
      if (serverErrorLogs.length > 100) {
        serverErrorLogs.shift();
      }
    });

    serverProcess.on('error', (err) => {
      console.error('Failed to start server:', err);
      reject(err);
    });

    serverProcess.on('close', (code) => {
      console.log(`Server process exited with code ${code}`);
      
      // 如果退出代码不是0，说明服务器异常退出
      if (code !== 0 && code !== null) {
        const errorDetails = serverErrorLogs.length > 0 
          ? serverErrorLogs.slice(-20).join('\n') 
          : '没有捕获到错误日志';
        
        // 分析错误类型
        const knownError = analyzeError(serverErrorLogs);
        
        let dialogOptions;
        if (knownError) {
          // 识别到已知错误，显示友好的错误信息和解决方案
          dialogOptions = {
            type: 'error',
            title: knownError.title,
            message: knownError.message,
            detail: `${knownError.solution}\n\n━━━━━━━━━━━━━━━━━━━━━━\n\n退出代码: ${code}\n\n错误类型: ${knownError.type}\n\n最近的错误日志:\n${errorDetails}`,
            buttons: ['退出应用', '查看完整日志'],
            defaultId: 0,
            cancelId: 0
          };
        } else {
          // 未识别的错误，显示通用错误信息
          dialogOptions = {
            type: 'error',
            title: '服务器崩溃',
            message: '服务器进程异常退出',
            detail: `退出代码: ${code}\n\n最近的错误信息:\n${errorDetails}`,
            buttons: ['退出应用', '查看完整日志'],
            defaultId: 0,
            cancelId: 0
          };
        }
        
        dialog.showMessageBox(dialogOptions).then((result) => {
          if (result.response === 1) {
            // 用户选择查看详情，保存并打开日志文件
            const logPath = saveAndOpenErrorLog();
            
            // 显示确认对话框
            const confirmMessage = logPath 
              ? `日志已保存到:\n${logPath}\n\n日志文件已在默认文本编辑器中打开。\n\n点击"退出"关闭应用程序。`
              : '日志保存失败，但已在控制台输出。\n\n点击"退出"关闭应用程序。';
            
            dialog.showMessageBox({
              type: 'info',
              title: '日志已保存',
              message: confirmMessage,
              buttons: ['退出'],
              defaultId: 0
            }).then(() => {
              app.isQuitting = true;
              app.quit();
            });
            
            // 同时在控制台输出
            console.log('=== 完整错误日志 ===');
            console.log(serverErrorLogs.join('\n'));
          } else {
            // 用户选择直接退出
            app.isQuitting = true;
            app.quit();
          }
        });
      } else {
        // 正常退出（code为0或null），直接关闭窗口
        if (mainWindow && !mainWindow.isDestroyed()) {
          mainWindow.close();
        }
      }
    });

    checkServerAvailability(PORT)
      .then(() => {
        console.log('✓ Backend server is accessible on port 3000');
        resolve();
      })
      .catch((err) => {
        console.error('✗ Failed to connect to backend server');
        reject(err);
      });
  });
}

function createWindow() {
  const isDev = process.env.NODE_ENV === 'development';
  const loadPort = isDev ? DEV_FRONTEND_PORT : PORT;
  
  mainWindow = new BrowserWindow({
    width: 1080,
    height: 720,
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      nodeIntegration: false,
      contextIsolation: true
    },
    title: 'New API',
    icon: path.join(__dirname, 'icon.png')
  });

  mainWindow.loadURL(`http://127.0.0.1:${loadPort}`);
  
  console.log(`Loading from: http://127.0.0.1:${loadPort}`);

  if (isDev) {
    mainWindow.webContents.openDevTools();
  }

  // Close to tray instead of quitting
  mainWindow.on('close', (event) => {
    if (!app.isQuitting) {
      event.preventDefault();
      mainWindow.hide();
      if (process.platform === 'darwin') {
        app.dock.hide();
      }
    }
  });

  mainWindow.on('closed', () => {
    mainWindow = null;
  });
}

function createTray() {
  // Use template icon for macOS (black with transparency, auto-adapts to theme)
  // Use colored icon for Windows
  const trayIconPath = process.platform === 'darwin'
    ? path.join(__dirname, 'tray-iconTemplate.png')
    : path.join(__dirname, 'tray-icon-windows.png');

  tray = new Tray(trayIconPath);

  const contextMenu = Menu.buildFromTemplate([
    {
      label: 'Show New API',
      click: () => {
        if (mainWindow === null) {
          createWindow();
        } else {
          mainWindow.show();
          if (process.platform === 'darwin') {
            app.dock.show();
          }
        }
      }
    },
    { type: 'separator' },
    {
      label: 'Quit',
      click: () => {
        app.isQuitting = true;
        app.quit();
      }
    }
  ]);

  tray.setToolTip('New API');
  tray.setContextMenu(contextMenu);

  // On macOS, clicking the tray icon shows the window
  tray.on('click', () => {
    if (mainWindow === null) {
      createWindow();
    } else {
      mainWindow.isVisible() ? mainWindow.hide() : mainWindow.show();
      if (mainWindow.isVisible() && process.platform === 'darwin') {
        app.dock.show();
      }
    }
  });
}

app.whenReady().then(async () => {
  try {
    await startServer();
    createTray();
    createWindow();
  } catch (err) {
    console.error('Failed to start application:', err);
    
    // 分析启动失败的错误
    const knownError = analyzeError(serverErrorLogs);
    
    if (knownError) {
      dialog.showMessageBox({
        type: 'error',
        title: knownError.title,
        message: `启动失败: ${knownError.message}`,
        detail: `${knownError.solution}\n\n━━━━━━━━━━━━━━━━━━━━━━\n\n错误信息: ${err.message}\n\n错误类型: ${knownError.type}`,
        buttons: ['退出', '查看完整日志'],
        defaultId: 0,
        cancelId: 0
      }).then((result) => {
        if (result.response === 1) {
          // 用户选择查看日志
          const logPath = saveAndOpenErrorLog();
          
          const confirmMessage = logPath 
            ? `日志已保存到:\n${logPath}\n\n日志文件已在默认文本编辑器中打开。\n\n点击"退出"关闭应用程序。`
            : '日志保存失败，但已在控制台输出。\n\n点击"退出"关闭应用程序。';
          
          dialog.showMessageBox({
            type: 'info',
            title: '日志已保存',
            message: confirmMessage,
            buttons: ['退出'],
            defaultId: 0
          }).then(() => {
            app.quit();
          });
          
          console.log('=== 完整错误日志 ===');
          console.log(serverErrorLogs.join('\n'));
        } else {
          app.quit();
        }
      });
    } else {
      dialog.showMessageBox({
        type: 'error',
        title: '启动失败',
        message: '无法启动服务器',
        detail: `错误信息: ${err.message}\n\n请检查日志获取更多信息。`,
        buttons: ['退出', '查看完整日志'],
        defaultId: 0,
        cancelId: 0
      }).then((result) => {
        if (result.response === 1) {
          // 用户选择查看日志
          const logPath = saveAndOpenErrorLog();
          
          const confirmMessage = logPath 
            ? `日志已保存到:\n${logPath}\n\n日志文件已在默认文本编辑器中打开。\n\n点击"退出"关闭应用程序。`
            : '日志保存失败，但已在控制台输出。\n\n点击"退出"关闭应用程序。';
          
          dialog.showMessageBox({
            type: 'info',
            title: '日志已保存',
            message: confirmMessage,
            buttons: ['退出'],
            defaultId: 0
          }).then(() => {
            app.quit();
          });
          
          console.log('=== 完整错误日志 ===');
          console.log(serverErrorLogs.join('\n'));
        } else {
          app.quit();
        }
      });
    }
  }
});

app.on('window-all-closed', () => {
  // Don't quit when window is closed, keep running in tray
  // Only quit when explicitly choosing Quit from tray menu
});

app.on('activate', () => {
  if (BrowserWindow.getAllWindows().length === 0) {
    createWindow();
  }
});

app.on('before-quit', (event) => {
  if (serverProcess) {
    event.preventDefault();

    console.log('Shutting down server...');
    serverProcess.kill('SIGTERM');

    setTimeout(() => {
      if (serverProcess) {
        serverProcess.kill('SIGKILL');
      }
      app.exit();
    }, 5000);

    serverProcess.on('close', () => {
      serverProcess = null;
      app.exit();
    });
  }
});
