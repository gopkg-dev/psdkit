// 静态任务数据是唯一状态来源；这里仅筛选 DOM、展开分组和定位任务。
function initializeTaskBoard() {
  const board = document.querySelector('[data-task-board-content]');
  if (!board) return;

  const controls = board.querySelector('[data-task-controls]');
  const search = board.querySelector('[data-task-search]');
  const status = board.querySelector('[data-task-status-filter]');
  const priority = board.querySelector('[data-task-priority-filter]');
  const results = board.querySelector('[data-task-results]');
  const empty = board.querySelector('[data-task-empty]');
  const announcement = board.querySelector('[data-task-announcement]');
  const normalize = (value) => value.normalize('NFKC').toLocaleLowerCase().replace(/\s+/gu, ' ').trim();
  const groups = [...board.querySelectorAll('[data-task-group]')].map((element) => {
    const groupText = `${element.dataset.groupId} ${element.dataset.groupTitle} ${element.querySelector('[data-group-summary]')?.textContent || ''}`;
    return {
      element,
      tasks: [...element.querySelectorAll('[data-task-id]')].map((task) => ({
        element: task,
        text: normalize(`${groupText} ${task.dataset.taskId} ${task.textContent}`),
      })),
    };
  });
  const total = groups.reduce((count, group) => count + group.tasks.length, 0);
  const statusLabels = { all: '全部状态', todo: '未完成', done: '已完成' };

  function filterTasks() {
    const terms = normalize(search.value).split(' ').filter(Boolean);
    const filtering = terms.length > 0 || status.value !== 'all' || priority.value !== 'all';
    let visibleTasks = 0;
    let visibleGroups = 0;
    for (const group of groups) {
      let groupMatches = 0;
      for (const task of group.tasks) {
        const matches = (status.value === 'all' || task.element.dataset.taskStatus === status.value)
          && (priority.value === 'all' || task.element.dataset.taskPriority === priority.value)
          && terms.every((term) => task.text.includes(term));
        task.element.hidden = !matches;
        if (matches) groupMatches += 1;
      }
      group.element.hidden = groupMatches === 0;
      if (groupMatches > 0) {
        visibleTasks += groupMatches;
        visibleGroups += 1;
        if (filtering) group.element.open = true;
      }
    }
    const labels = [];
    if (terms.length > 0) labels.push(`关键词“${search.value.trim()}”`);
    if (status.value !== 'all') labels.push(statusLabels[status.value]);
    if (priority.value !== 'all') labels.push(priority.value);
    const suffix = labels.length > 0 ? ` · ${labels.join(' / ')}` : '';
    results.textContent = `显示 ${visibleTasks} / ${total} 项 · ${visibleGroups} / ${groups.length} 组${suffix}`;
    empty.hidden = visibleTasks !== 0;
  }

  function clearFilters() {
    search.value = '';
    status.value = 'all';
    priority.value = 'all';
    filterTasks();
  }

  function revealHashTarget() {
    let id;
    try { id = decodeURIComponent(location.hash.slice(1)); } catch { return; }
    if (!id) return;
    const target = document.getElementById(id);
    if (!target || !board.contains(target)) return;
    const task = target.closest('[data-task-id]');
    const group = target.closest('[data-task-group]');
    if (!group) return;
    const cleared = group.hidden || task?.hidden;
    if (cleared) clearFilters();
    group.open = true;
    const title = task?.querySelector('h3')?.textContent || group.dataset.groupTitle;
    announcement.textContent = `${cleared ? '已清除筛选，' : ''}已展开并定位到“${title}”。`;
    requestAnimationFrame(() => {
      if (task) task.focus({ preventScroll: true });
      target.scrollIntoView({ block: 'start', behavior: 'instant' });
    });
  }

  search.addEventListener('input', filterTasks);
  status.addEventListener('change', filterTasks);
  priority.addEventListener('change', filterTasks);
  board.querySelectorAll('[data-task-reset]').forEach((button) => button.addEventListener('click', () => {
    clearFilters();
    search.focus({ preventScroll: true });
    announcement.textContent = '已清除全部筛选。';
  }));
  board.querySelector('[data-task-expand]').addEventListener('click', () => {
    groups.forEach((group) => { group.element.open = true; });
    announcement.textContent = '已展开全部分组，筛选条件保持不变。';
  });
  board.querySelector('[data-task-collapse]').addEventListener('click', () => {
    groups.forEach((group) => { group.element.open = false; });
    announcement.textContent = '已收起全部分组，筛选条件保持不变。';
  });
  board.addEventListener('click', (event) => {
    const link = event.target.closest('a[href^="#"]');
    if (link && link.hash === location.hash) {
      event.preventDefault();
      revealHashTarget();
    }
  });
  window.addEventListener('hashchange', revealHashTarget);
  controls.hidden = false;
  filterTasks();
  revealHashTarget();
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', initializeTaskBoard, { once: true });
} else {
  initializeTaskBoard();
}
