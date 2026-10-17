const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'web/assets/workspace.js'),'utf8');
const context={friendlyMeta:x=>x,esc:x=>String(x).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('"','&quot;'),currentRelease:()=>({})};
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('function fileStatusLabel('),source.indexOf('function releaseLinks(')),context);
vm.runInContext(source.slice(source.indexOf('function batchRows('),source.indexOf('function renderBatch(')),context);

function fixture(status='running'){
 return {id:'synthetic',status,current_path:'/media/working.mp4',files:['/media/completed.mp4','/media/existing.mp4','/media/failed.mp4','/media/working.mp4','/media/pending.mp4'],results:[{input:'/media/completed.mp4'},{input:'/media/existing.mp4',skipped:true}],file_failures:[{path:'/media/failed.mp4',error:'Synthetic failure'}]};
}

test('file outcomes separate new output, existing output, failure, current and pending',()=>{
 const rows=context.batchRows(fixture());
 assert.deepEqual(Array.from(rows,row=>row.status),['completed','skipped','failed','running','remaining']);
 const counts=context.fileOutcomeCounts(fixture());
 for(const key of ['completed','skipped','failed','running','remaining'])assert.equal(counts[key],1,key);
 assert.equal(counts.not_processed,0);
});

test('failed and cancelled batches do not mark untouched files failed',()=>{
 for(const status of ['failed','cancelled']){
  const rows=context.batchRows(fixture(status));
  assert.deepEqual(Array.from(rows,row=>row.status),['completed','skipped','failed','not_processed','not_processed']);
  const counts=context.fileOutcomeCounts(fixture(status));
  assert.equal(counts.failed,1);assert.equal(counts.not_processed,2);
 }
});

test('summary and full detail counts and labels agree',()=>{
 const full=context.fileOutcomeCounts(fixture());
 const summary=context.fileOutcomeCounts({id:'synthetic',status:'running',current_path:'/media/working.mp4',file_count:5,generated_count:1,skipped_count:1,failure_count:1});
 assert.deepEqual(full,summary);
 const html=context.fileOutcomeSummary(fixture());
 for(const label of ['Completed','Skipped','Failed','Processing','Pending','Not processed'])assert.ok(html.includes(label));
 assert.ok(html.includes('data-file-status="failed"'));
});

test('recent filenames are escaped and have the same outcome labels',()=>{
 const html=context.recentFileResults({recent_file_results:[{path:'/media/<example>.mp4',filename:'<example>.mp4',status:'failed'}]});
 assert.ok(html.includes('Failed'));assert.ok(html.includes('&lt;example>.mp4'));assert.ok(!html.includes('<example>'));
});

test('dashboard collapses an empty queue and restores the rail for upcoming releases',()=>{
 const jobs={dataset:{},innerHTML:''};
 const state={dashboardIndex:0,activityPage:1,activityPages:1,activityRecords:[],el:id=>id==='jobs'?jobs:{value:'25'},activityCard:()=>'<article>Current job</article>',cover:()=>'<div>Cover</div>',esc:context.esc};
 vm.createContext(state);
 vm.runInContext(source.slice(source.indexOf('function renderDashboard('),source.indexOf('function switchDashboardJob(')),state);
 for(const job of [
  {id:'synthetic',status:'running',file_count:1,next_files:[]},
  {id:'synthetic',status:'queued',phase:'matching_releases',next_files:[]},
  {id:'synthetic',status:'running',file_count:2,next_files:[{path:'/media/next.mp4',metadata:{release_title:'Example upcoming release'}}]},
 ]){
  state.activityRecords=[job];state.renderDashboard();
  assert.equal(jobs.innerHTML.includes('dashboard-content queue-empty'),job.next_files.length===0);
  assert.ok(jobs.innerHTML.includes('data-inspect="synthetic"'));
  if(job.phase)assert.ok(jobs.innerHTML.includes('Checking eligibility'));
  if(job.next_files.length)assert.ok(jobs.innerHTML.includes('Example upcoming release'));
 }
 state.activityRecords=[];state.renderDashboard();
 assert.ok(jobs.innerHTML.includes('Nothing processing right now'));
});
