const status = document.querySelector("#connection-status");

fetch("/api/v1/health")
  .then((response) => response.json())
  .then((health) => {
    status.textContent = `服务已连接 · ${health.version}`;
  })
  .catch(() => {
    status.textContent = "无法连接本地服务";
  });
