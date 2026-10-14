/* Activity is intentionally separate from full job inspection. */
let workspaceView='dashboard', configurationView='settings', activityRequest=0, activityAbort;
let activityRecords=[], detailJob=null, detailPage=1, detailFilter='', detailStatus='all', detailRequest=0;
let dashboardIndex=0, historyLoading=false, historyGeneration=0, batchObserver, historyObserver, historyTailNode;
const configurationSections=['settings','profiles','models','setup'];
const safeURL=value=>{try{const url=new URL(value,location.origin);return value&&/^https?:$/.test(url.protocol)?url.href:''}catch{return''}};

function installWorkspace(){
  const nav=document.querySelector('.nav');
  nav.innerHTML='<button id="dashboardNav" onclick="showTab(\'dashboard\')">Dashboard</button><button id="historyNav" onclick="showTab(\'history\')">Job history</button><button id="newJobNav" onclick="showTab(\'newJob\')">New subtitle job</button><button id="schedulesNav" onclick="showTab(\'schedules\')">Schedules</button><button id="configurationNav" onclick="showTab(\'configuration\')">Configuration</button>';
  const icons = [
    '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',
    '<path d="M3 11a9 9 0 1 1 2.7 7M3 4v7h7M12 7v5l3 2"/>',
    '<rect x="4" y="6" width="16" height="15" rx="2"/><path d="M9 6V3h6v3M12 10v7M8.5 13.5h7"/>',
    '<rect x="3" y="5" width="18" height="16" rx="2"/><path d="M7 3v4M17 3v4M3 11h18M8 15h2M14 15h2"/>',
    '<path d="m9 3-1 3-3 1v3l-2 2 2 2v3l3 1 1 3h6l1-3 3-1v-3l2-2-2-2V7l-3-1-1-3Z"/><circle cx="12" cy="12" r="3"/>'
  ];
  [...nav.querySelectorAll('button')].forEach((button,i)=>button.insertAdjacentHTML('afterbegin',`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${icons[i]}</svg>`));
  const brand=document.querySelector('.top .brand');
  brand.classList.add('sidebar-brand');nav.prepend(brand);
  const composer=document.querySelector('.job-composer'), section=document.createElement('section');
  section.id='newJobPanel';section.className='hidden';section.append(composer);el('jobsPanel').after(section);
  const heading=document.createElement('div');heading.id='workspaceHeading';heading.className='workspace-heading';nav.after(heading);
  const metrics=document.createElement('div');metrics.id='overviewMetrics';metrics.className='overview-metrics';document.querySelector('.topright').before(metrics);
  const tail=document.createElement('div');tail.id='historyTail';historyTailNode=tail;el('jobs').after(tail);
  historyObserver=new IntersectionObserver(entries=>{if(entries.some(entry=>entry.isIntersecting))loadMoreHistory()},{root:el('jobs'),rootMargin:'200px'});
  const subtabs=document.createElement('div');subtabs.id='configurationTabs';subtabs.className='config-tabs hidden';
  subtabs.innerHTML=configurationSections.map((name,i)=>`<button data-config="${name}">${['Translation & integrations','Recognition & vocabulary','Models','API & installation'][i]}</button>`).join('');
  heading.after(subtabs);subtabs.addEventListener('click',event=>{const name=event.target.closest('[data-config]')?.dataset.config;if(name){configurationView=name;showTab('configuration')}});
  el('activityPageSize').value='25';el('activityFilter').placeholder='Search jobs, release titles or filenames…';
  const dialog=document.createElement('dialog');dialog.id='jobDialog';dialog.className='job-dialog';dialog.innerHTML='<div class="dialog-heading"><div><span class="eyebrow">Job details</span><h2 id="detailTitle"></h2></div><button class="ghost" id="closeJobDetails" aria-label="Close job details">Close ✕</button></div><div class="dialog-body" id="detailBody"></div>';
  document.body.append(dialog);el('closeJobDetails').onclick=()=>dialog.close();dialog.addEventListener('close',()=>{detailRequest++;detailJob=null;batchObserver?.disconnect()});
  el('jobs').addEventListener('click',event=>{const button=event.target.closest('[data-inspect]');if(button)inspectJob(button.dataset.inspect)});
  el('jobs').addEventListener('change',event=>{if(event.target.matches('[data-select-job]'))toggleJobSelection(event.target.dataset.selectJob,event.target.checked)});
  dialog.addEventListener('click',event=>{const refresh=event.target.closest('[data-refresh-job]');if(refresh&&detailJob)inspectJob(detailJob.id);if(event.target.closest('[data-batch-more]')){detailPage++;renderBatch(true)}});
  dialog.addEventListener('input',event=>{if(event.target.id==='detailSearch'){detailFilter=event.target.value;detailPage=1;renderBatch()}});
  dialog.addEventListener('change',event=>{if(event.target.id==='detailStatus'){detailStatus=event.target.value;detailPage=1;renderBatch()}});
  installScheduleEditor();showTab('dashboard');
}

showTab=function(view){
  if(view==='jobs')view='dashboard';
  if(configurationSections.includes(view)){configurationView=view;view='configuration'}
  workspaceView=view;
  el('app').classList.toggle('dashboard-view',view==='dashboard');el('app').classList.toggle('history-view',view==='history');
  dashboardIndex=0;historyGeneration++;
  el('jobs').dataset.dashboardHTML='';
  if(view==='dashboard'||view==='history')el('jobs').replaceChildren();
  const panel=view==='dashboard'||view==='history'?'jobs':view==='configuration'?configurationView:view;
  ['jobs','newJob','schedules',...configurationSections].forEach(name=>el(name+'Panel').classList.toggle('hidden',name!==panel));
  ['dashboard','history','newJob','schedules','configuration'].forEach(name=>el(name+'Nav').classList.toggle('active',name===view));
  el('configurationTabs').classList.toggle('hidden',view!=='configuration');
  document.querySelectorAll('[data-config]').forEach(button=>button.classList.toggle('active',button.dataset.config===configurationView));
  const titles={dashboard:['Dashboard','Follow subtitle processing and see what is next.'],history:['Job history','Review completed batches, failed files and subtitle downloads.'],newJob:['New subtitle job','Choose your source and the subtitle tracks you want to create.'],schedules:['Schedules','Manage recurring scans and upcoming subtitle jobs.'],configuration:['Configuration','Manage translation, recognition and connected services.']};
  const [title,description]=titles[view]||titles.dashboard;
  el('workspaceHeading').innerHTML=`<div><span class="eyebrow">JAVBeacon Subtitles</span><h2>${title}</h2><p>${description}</p></div>${view==='schedules'?'<button onclick="openScheduleEditor()">＋ New schedule</button>':['dashboard','history'].includes(view)?'<button onclick="showTab(\'newJob\')">＋ New job</button>':''}`;
  el('overviewMetrics').classList.toggle('hidden',view!=='dashboard');
  el('activityStatusTabs').classList.toggle('hidden',view!=='history');
  if(view==='dashboard'||view==='history'){
    if(view==='dashboard')activityStatusFilter='active';
    else if(activityStatusFilter==='active')activityStatusFilter='completed';
    activityPage=1;updateActivityTabs();loadJobs();
  }
  if(panel==='schedules')loadSchedules();
  if(panel==='settings')loadSettings();
  if(panel==='profiles'){loadProfiles();loadCatalogs()}
  if(panel==='models')loadModels();
  if(panel==='setup')setupExample();
};

function releaseLinks(metadata){return [['release_url','JAVBeacon ↗'],['release_stash_url','Stash ↗']].map(([key,title])=>{const url=safeURL(metadata[key]);return url?`<a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${title}</a>`:''}).join('')}
function cover(metadata,small=false){const releaseID=metadata.javbeacon_release_id||String(metadata.release_url||'').match(/\/release\/(\d+)/)?.[1],url=releaseID?'/api/v1/covers/'+encodeURIComponent(releaseID):safeURL(metadata.release_image_url);return `<div class="release-cover${small?' small':''}"><span class="cover-placeholder">${esc(metadata.release_external_id||'Cover unavailable')}</span>${url?`<img src="${esc(url)}" alt="Release cover" loading="lazy" decoding="async" referrerpolicy="no-referrer" onload="this.parentElement.classList.toggle(\'landscape\',this.naturalWidth>this.naturalHeight)" onerror="this.remove()">`:''}</div>`}
function activityCard(job){
  const metadata=currentRelease(job),active=['running','queued'].includes(job.status),total=job.file_count||0,done=job.completed_count||0;
  const progress=Math.max(0,Math.min(100,Number(job.progress)||0)),title=metadata.release_title||metadata.release_external_id||job.current_file||job.external_id||'Subtitle batch';
  const current=job.current_file||job.current_path?.split(/[\\/]/).pop()||'';
  const phase=job.phase||job.status,preparing=['scanning','matching_releases','preparing'].includes(phase)&&job.status==='queued';
  const fileCaption=preparing?('Checking '+(job.current_file_number||0)+' / '+(job.discovered_count||0)+' files'):(done+' / '+total+' files');
  return `<article class="activity-job status-${esc(job.status)}" data-job="${esc(job.id)}"><header class="jobhead"><strong><input class="job-select" type="checkbox" aria-label="Select job ${esc(job.external_id||job.id)}" data-select-job="${esc(job.id)}" ${selectedJobIDs.has(job.id)?'checked':''}>${esc(job.external_id||job.schedule_name||job.id)}</strong>${job.schedule_name?'<span class="schedule-origin">Scheduled</span>':''}<span class="pill ${esc(job.status)}">${esc(friendlyMeta(job.status))}</span></header><div class="activity-job-body"><div class="now-playing">${cover(metadata)}<div><span class="eyebrow">${preparing?'Preparing batch':job.status==='running'?'Processing now':job.status==='queued'?'Waiting in queue':'Subtitle batch'}</span><h3 class="current-video-title">${esc(title)}</h3>${current?`<div class="current-filename">${esc(current)}</div>`:''}<div class="release-tags">${metadata.release_external_id?`<span>${esc(metadata.release_external_id)}</span>`:''}${metadata.release_provider?`<span>${esc(metadata.release_provider)}</span>`:''}${metadata.release_date?`<span>${esc(metadata.release_date)}</span>`:''}${metadata.release_lookup_ambiguous?'<span>⚠ Review ambiguous match</span>':''}</div><div class="release-actions">${releaseLinks(metadata)}</div><div class="bar" role="progressbar" aria-label="Job progress" aria-valuenow="${progress}" aria-valuemin="0" aria-valuemax="100"><div class="fill" style="width:${progress}%"></div></div><div class="progress-caption"><strong>${esc(friendlyMeta(phase))} · ${progress.toFixed(0)}%</strong><span>${esc(preparing?fileCaption:eta(job)||`${done} of ${total} files finished`)}</span></div><p class="job-stage-message">${esc(job.message||'Waiting for processing')}${job.error?`<br><span class="error">${esc(job.error)}</span>`:''}</p></div></div><footer class="job-footer"><div class="job-facts"><span><b>${esc(fileCaption)}</b></span><span>${esc(friendlyMeta(job.asr_mode||'Auto'))}</span><span>${esc(friendlyMeta(job.profile||job.asr_profile||'Auto'))}</span>${job.failure_count?`<span class="error">${job.failure_count} failed files</span>`:''}<span>${esc(formatJobDate(job.created_at))}</span></div><div class="actions"><button class="ghost" data-inspect="${esc(job.id)}">${active?'View batch':'Details & downloads'}</button>${active?`<button class="danger" data-cancel="${esc(job.id)}">Cancel</button>`:''}</div></footer></div></article>`;
}

render=function(list){
  activityRecords=list;visibleJobIDs=list.map(job=>job.id);
  if(workspaceView==='dashboard'){renderDashboard();updateSelectedCount();return}
  const container=el('jobs'),existing=new Map([...container.querySelectorAll('[data-job]')].map(node=>[node.dataset.job,node]));
  container.querySelectorAll('.dashboard-empty,.empty').forEach(node=>node.remove());
  for(const job of list){const html=activityCard(job),old=existing.get(job.id);if(old?.dataset.rendered===html){container.append(old)}else{const template=document.createElement('template');template.innerHTML=html;const node=template.content.firstElementChild;node.dataset.rendered=html;old?.remove();container.append(node)}existing.delete(job.id)}
  existing.forEach(node=>node.remove());
  if(!list.length)container.innerHTML=`<div class="dashboard-empty"><strong>${workspaceView==='dashboard'?'Nothing processing right now':'No matching jobs'}</strong>${workspaceView==='dashboard'?'Start a subtitle job or set up a recurring scan.':'Try another search or status filter.'}</div>`;
  updateSelectedCount();
};
el('jobs').addEventListener('click',event=>{const button=event.target.closest('[data-cancel]');if(button)cancelJob(button.dataset.cancel)});

loadJobs=async function(){
  if(!['dashboard','history'].includes(workspaceView)||document.hidden||el('app').classList.contains('hidden'))return;
  activityAbort?.abort();const controller=new AbortController();activityAbort=controller;const request=++activityRequest;
  if(workspaceView==='history')historyGeneration++;
  const query=new URLSearchParams({view:'summary',page:activityPage,page_size:el('activityPageSize').value,filter:el('activityFilter').value,status:activityStatusFilter});
  try{
    const data=await api('/api/v1/jobs?'+query,{signal:controller.signal});if(request!==activityRequest)return;
    activityPage=data.page||1;activityPages=Math.max(1,data.pages||1);
    if(activityPage>activityPages){activityPage=activityPages;loadJobs();return}
    render(data.jobs||[]);el('activityPage').textContent=`${data.total||0} jobs · ${workspaceView==='dashboard'?'Page '+activityPage+' of '+activityPages:'Scroll to load more'}`;
    el('activityPrevious').disabled=activityPage<=1;el('activityNext').disabled=activityPage>=activityPages;
    const records=data.jobs||[],running=records.filter(job=>job.status==='running').length,queued=records.filter(job=>job.status==='queued').length;
    el('overviewMetrics').innerHTML=[['Active jobs',data.total||0,'All queued and running jobs'],['Processing',running,'On this page'],['Queued',queued,'On this page'],['Files remaining',records.reduce((sum,job)=>sum+Math.max(0,(job.file_count||0)-(job.completed_count||0)-(job.failure_count||0)),0),'Across jobs on this page']].map(([label,count,caption])=>`<div class="overview-metric"><span>${label}</span><strong>${count}</strong><small>${caption}</small></div>`).join('');
    el('activityError')?.remove();
    if(workspaceView==='history')updateHistoryTail();
  }catch(error){if(error.name==='AbortError'||request!==activityRequest)return;let notice=el('activityError');if(!notice){notice=document.createElement('p');notice.id='activityError';notice.className='page-error';notice.setAttribute('role','alert');el('jobs').before(notice)}notice.textContent='Activity could not be refreshed: '+error.message}
};

async function inspectJob(id){
  const request=++detailRequest,dialog=el('jobDialog');detailJob=null;detailPage=1;detailFilter='';detailStatus='all';
  el('detailTitle').textContent='Loading job…';el('detailBody').innerHTML='<p class="muted">Loading release details and subtitle outputs…</p>';if(!dialog.open)dialog.showModal();
  try{const job=await api('/api/v1/jobs/'+encodeURIComponent(id));if(request!==detailRequest||!dialog.open)return;detailJob=job;detailStatus='all';renderDetails()}catch(error){if(request===detailRequest)el('detailBody').textContent=error.message}
}
function renderDetails(){
  const job=detailJob;if(!job)return;el('detailTitle').textContent=job.external_id||job.schedule_name||job.id;
  el('detailBody').innerHTML=`<div class="progress-caption"><strong>${esc(friendlyMeta(job.status))} · ${job.progress||0}%</strong><button class="ghost" data-refresh-job>Refresh details</button></div><p>${esc(job.message||'')}</p>${job.error?`<p class="error">${esc(job.error)}</p>`:''}${jobTiming(job)}${jobTranslationUsage(job)}<div class="batch-toolbar"><input id="detailSearch" aria-label="Search batch files" placeholder="Search release or filename" value="${esc(detailFilter)}"><select id="detailStatus" aria-label="Filter batch files"><option value="all">All included files</option><option value="remaining">Remaining</option><option value="completed">Completed</option><option value="failed">Failed</option></select></div><div id="batchReleases" class="batch-release-grid"></div><div id="batchPager" class="batch-pager"></div>`;
  el('detailStatus').value=detailStatus;renderBatch();
}
function batchRows(job){
  const results=new Map((job.results||[]).map((result,index)=>[result.input,{result,index}])),failures=new Map((job.file_failures||[]).map(item=>[item.path,item]));
  const scanned=job.scan_results?.length?job.scan_results:(job.files||[]).map(path=>({path,filename:path.split(/[\\/]/).pop(),included:true}));
  return scanned.filter(row=>row.included).map(row=>{const output=results.get(row.path),failure=failures.get(row.path);let status=output?'completed':failure?'failed':!row.included?'excluded':row.path===job.current_path&&job.status==='running'?'running':['failed','cancelled','skipped'].includes(job.status)?job.status:'remaining';return {...row,metadata:job.file_release_metadata?.[row.path]||currentRelease({...job,current_path:row.path}),output,failure,status}});
}
function renderBatch(append=false){
  const job=detailJob;if(!job)return;const query=detailFilter.trim().toLowerCase(),rows=batchRows(job).filter(row=>(detailStatus==='all'||row.status===detailStatus)&&(row.filename+' '+row.path+' '+row.metadata.release_title+' '+row.metadata.release_external_id).toLowerCase().includes(query));
  const pages=Math.max(1,Math.ceil(rows.length/12));detailPage=Math.max(1,Math.min(detailPage,pages));
  const cards=rows.slice(append?(detailPage-1)*12:0,detailPage*12).map(row=>{
    const metadata=row.metadata;
    const skippedLookup=row.lookup_method==='eligibility_prefilter'||metadata.release_lookup_method==='eligibility_prefilter';
    const showCover=!skippedLookup&&(metadata.javbeacon_release_id||metadata.release_url||metadata.release_image_url);
    return `<article class="batch-file${showCover?'':' no-cover'}"><div class="batch-file-top">${showCover?cover(metadata,true):''}<div><h3>${esc(metadata.release_title||metadata.release_external_id||row.filename)}</h3><span class="pill ${esc(row.status)}">${esc(friendlyMeta(row.status))}</span><p>${esc(row.filename)}</p><div class="release-actions">${releaseLinks(metadata)}</div></div></div>${row.failure?`<p class="error">${esc(row.failure.error)}</p>`:''}${row.reason?`<p>${esc(row.reason)}</p>`:''}${metadata.release_lookup_ambiguous?'<p class="error">Ambiguous release match — review the selected release.</p>':''}${metadata.release_story?`<details><summary>Release story</summary><p>${esc(metadata.release_story)}</p></details>`:''}<details><summary>File path & matching</summary><p>${esc(row.path)}</p><p>${esc(friendlyMeta(metadata.release_lookup_method||'No release match'))}</p></details>${row.output?`<div class="output-links">${outputArtifactLinks(job,row.output.result,row.output.index)}</div>`:''}</article>`;
  }).join('')||'<p class="muted">No files match this filter.</p>';
  if(append)el('batchReleases').insertAdjacentHTML('beforeend',cards);else el('batchReleases').innerHTML=cards;
  el('batchPager').innerHTML=`<span>${Math.min(rows.length,detailPage*12)} of ${rows.length} files</span>${detailPage<pages?'<button data-batch-more>Load more releases</button>':'<span>All matching files shown</span>'}`;
  batchObserver?.disconnect();if(detailPage<pages){batchObserver=new IntersectionObserver(entries=>{if(entries.some(entry=>entry.isIntersecting)){batchObserver.disconnect();detailPage++;renderBatch(true)}},{root:el('detailBody'),rootMargin:'150px'});batchObserver.observe(el('batchPager'))}
}

function renderDashboard(){
  dashboardIndex=Math.min(dashboardIndex,Math.max(0,activityRecords.length-1));const job=activityRecords[dashboardIndex];
  if(!job){el('jobs').dataset.dashboardHTML='';el('jobs').innerHTML='<div class="dashboard-empty"><strong>Nothing processing right now</strong>Start a subtitle job or set up a recurring scan.</div>';return}
  const next=job.next_files||[],other=activityRecords.filter(row=>row.id!==job.id);
  const preparing=job.status==='queued'&&['scanning','matching_releases','preparing'].includes(job.phase);
  const side=`<aside class="up-next"><div class="up-next-heading"><h3>Up next</h3><span>${preparing?'Checking eligibility':Math.max(0,(job.file_count||0)-(job.current_file_number||0))+' remaining'}</span></div><div class="next-releases">${next.map(row=>`<article class="next-release">${cover(row.metadata,true)}<div><strong>${esc(row.metadata.release_title||row.metadata.release_external_id||row.path.split(/[\\/]/).pop())}</strong><span>${esc(row.metadata.release_external_id||row.path.split(/[\\/]/).pop())}</span></div></article>`).join('')||(preparing?'<p class="muted">Eligible files appear here after matching.</p>':'<p class="muted">No further files in this batch.</p>')}</div><button class="ghost" data-inspect="${esc(job.id)}">${preparing?'View batch':'View '+(job.file_count||0)+' included releases'}</button><div class="queued-note">${other.length} other job${other.length===1?'':'s'} on this page</div></aside>`;
  const html=`<div class="dashboard-content"><div class="dashboard-focus"><div class="focus-switcher"><strong>Current activity</strong><div><button class="ghost" onclick="switchDashboardJob(-1)" ${dashboardIndex===0&&activityPage===1?'disabled':''} aria-label="Previous job">‹</button><span>Job ${(activityPage-1)*Number(el('activityPageSize').value)+dashboardIndex+1}</span><button class="ghost" onclick="switchDashboardJob(1)" ${dashboardIndex===activityRecords.length-1&&activityPage===activityPages?'disabled':''} aria-label="Next job">›</button></div></div>${activityCard(job)}</div>${side}</div>`;
  if(el('jobs').dataset.dashboardHTML!==html){el('jobs').innerHTML=html;el('jobs').dataset.dashboardHTML=html}
}
function switchDashboardJob(direction){const next=dashboardIndex+direction;if(next<0&&activityPage>1){dashboardIndex=24;activityPage--;loadJobs()}else if(next>=activityRecords.length&&activityPage<activityPages){dashboardIndex=0;activityPage++;loadJobs()}else{dashboardIndex=Math.max(0,Math.min(activityRecords.length-1,next));renderDashboard()}}
function updateHistoryTail(){historyTailNode.innerHTML=activityPage<activityPages?'<button onclick="loadMoreHistory()">Load more jobs</button>':'<span>All matching jobs shown</span>';el('jobs').append(historyTailNode);historyObserver.disconnect();if(activityPage<activityPages)historyObserver.observe(historyTailNode)}
async function loadMoreHistory(){
  if(workspaceView!=='history'||historyLoading||activityPage>=activityPages)return;historyLoading=true;const generation=historyGeneration;
  const query=new URLSearchParams({view:'summary',page:activityPage+1,page_size:el('activityPageSize').value,filter:el('activityFilter').value,status:activityStatusFilter});
  try{const data=await api('/api/v1/jobs?'+query);if(generation!==historyGeneration)return;activityPage=data.page;activityPages=Math.max(1,data.pages||1);const records=new Map([...activityRecords,...data.jobs||[]].map(job=>[job.id,job]));render([...records.values()]);updateHistoryTail()}catch(error){if(generation===historyGeneration)historyTailNode.innerHTML='<span class="error">Could not load more jobs.</span><button onclick="loadMoreHistory()">Retry</button>'}finally{historyLoading=false}
}

const submitWorkspaceJob=submitJob;
submitJob=async function(){await submitWorkspaceJob();if(!el('formError').textContent)showTab('dashboard')};
selectVisibleJobs=function(){visibleJobIDs.forEach(id=>selectedJobIDs.add(id));document.querySelectorAll('[data-select-job]').forEach(input=>input.checked=true);updateSelectedCount()};
clearJobSelection=function(){selectedJobIDs.clear();document.querySelectorAll('[data-select-job]').forEach(input=>input.checked=false);updateSelectedCount()};

function installScheduleEditor(){
  const form=el('scheduleFormTitle').closest('.card'),dialog=document.createElement('dialog');dialog.id='scheduleDialog';dialog.className='job-dialog schedule-dialog';
  const heading=document.createElement('div');heading.className='dialog-heading';heading.append(el('scheduleFormTitle'));const close=document.createElement('button');close.className='ghost';close.textContent='Close ✕';close.onclick=()=>dialog.close();heading.append(close);dialog.append(heading);
  const tabs=document.createElement('div');tabs.className='config-tabs schedule-editor-tabs';tabs.innerHTML=['Source','Timing','Processing','Release filters'].map((label,i)=>`<button data-schedule-step="${i}">${label}</button>`).join('');dialog.append(tabs);
  const body=document.createElement('div');body.className='dialog-body';dialog.append(body);body.append(form);document.body.append(dialog);
  const grid=form.querySelector('.schedule-form-grid'),options=form.querySelector('.schedule-options');
  const panes=['Source','Timing','Processing','Release filters'].map((label,i)=>{const pane=document.createElement('section');pane.className='schedule-step hidden';pane.dataset.step=i;pane.innerHTML=`<p class="schedule-step-intro">${['Choose the paths to scan and give this schedule a recognizable name.','Set a repeat pattern, time and timezone.','Choose recognition and output tracks.','Choose which JAVBeacon releases this schedule should include.'][i]}</p>`;form.insertBefore(pane,grid);return pane});
  for(const id of ['scheduleName','scheduleInputs'])panes[0].append(el(id).parentElement);
  [...grid.children].forEach(field=>{const processing=field.contains(el('scheduleASRMode'))||field.contains(el('scheduleProfile'));(processing?panes[2]:panes[1]).append(field)});
  panes[2].append(options);
  const callbackLabel=form.querySelector('label[for="scheduleCallback"]');const advanced=document.createElement('details');advanced.className='schedule-callback';advanced.innerHTML='<summary>Completion callback (optional)</summary>';advanced.append(callbackLabel,el('scheduleCallback'));panes[2].append(advanced);
  grid.remove();form.querySelector(':scope > .help')?.remove();
  tabs.addEventListener('click',event=>{const button=event.target.closest('[data-schedule-step]');if(button)showScheduleStep(Number(button.dataset.scheduleStep))});
  const recent=el('scheduleOnlyNew')?.closest('label');if(recent){recent.classList.add('recent-schedule-label');const container=[...options.children].find(child=>child.contains(recent));container?.classList.add('schedule-recent-option')}
  const sourceFlags=document.createElement('div');sourceFlags.className='schedule-options';panes[0].append(sourceFlags);
  for(const id of ['scheduleEnabled','scheduleRecursive'])sourceFlags.append(el(id).closest('label'));
  const releaseFlags=document.createElement('div');releaseFlags.className='schedule-options';panes[3].append(releaseFlags);
  for(const id of ['scheduleAutoRelease','scheduleOnlyWatchlist'])releaseFlags.append(el(id).closest('label'));
  const recentContainer=options.querySelector('.schedule-recent-option');if(recentContainer)releaseFlags.append(recentContainer);
  const advancedOptions=document.createElement('details');advancedOptions.className='schedule-callback';advancedOptions.innerHTML='<summary>Replacement & diagnostics</summary>';const advancedFlags=document.createElement('div');advancedFlags.className='schedule-options';advancedOptions.append(advancedFlags);
  for(const id of ['scheduleOverwrite','scheduleOverwriteIfModelNewer','scheduleOverwriteUnverifiedProvenance','scheduleDebug'])advancedFlags.append(el(id).closest('label'));
  panes[2].insertBefore(advancedOptions,advanced);
  const currentEditSchedule=editSchedule;editSchedule=function(id){currentEditSchedule(id);showScheduleStep(0);dialog.showModal()};
  const currentSaveSchedule=saveSchedule;saveSchedule=async function(){await currentSaveSchedule();if(!el('scheduleFormError').textContent)dialog.close()};
  const cancel=el('cancelScheduleEdit');cancel.onclick=()=>dialog.close();
}
function showScheduleStep(step){document.querySelectorAll('.schedule-step').forEach(pane=>pane.classList.toggle('hidden',Number(pane.dataset.step)!==step));document.querySelectorAll('[data-schedule-step]').forEach(button=>button.classList.toggle('active',Number(button.dataset.scheduleStep)===step))}
function openScheduleEditor(){resetScheduleForm();showScheduleStep(0);el('scheduleDialog').showModal()}
installWorkspace();
let lastActivityPoll=0;
setInterval(()=>{const now=Date.now();if(workspaceView==='dashboard'&&now-lastActivityPoll>=5000&&!el('jobDialog').open){lastActivityPoll=now;loadJobs()}},5000);
document.addEventListener('visibilitychange',()=>{if(!document.hidden&&workspaceView==='dashboard')loadJobs()});
