(()=>{
  const dialog=document.getElementById("spotlight-search");
  const trigger=document.querySelector("[data-search-open]");
  const form=dialog?.querySelector("[data-spotlight-form]");
  const input=dialog?.querySelector("#spotlight-query");
  const results=dialog?.querySelector("[data-search-results]");
  const status=dialog?.querySelector("[data-search-status]");
  if(!dialog||!trigger||!form||!input||!results||!status)return;
  let timer=0,request=null,generation=0;
  const searchRoute=()=>location.pathname==="/search";
  const focusInput=()=>setTimeout(()=>input.focus({preventScroll:true}),20);
  const open=()=>{
    if(!dialog.open){try{dialog.showModal()}catch{dialog.setAttribute("open","")}}
    trigger.setAttribute("aria-expanded","true");
    focusInput();
  };
  trigger.addEventListener("click",event=>{event.preventDefault();open()});
  document.addEventListener("keydown",event=>{
    if((event.metaKey||event.ctrlKey)&&event.key.toLowerCase()==="k"&&!event.isComposing){event.preventDefault();open()}
  });
  dialog.addEventListener("keydown",event=>{
    if(event.key==="Escape"&&!event.isComposing){
      event.preventDefault();event.stopPropagation();
      if(searchRoute())location.replace("/");else dialog.close();
    }
  });
  let backdropPointerDown=false;
  dialog.addEventListener("pointerdown",event=>{backdropPointerDown=event.target===dialog});
  document.addEventListener("pointerup",event=>{if(event.target!==dialog)backdropPointerDown=false},true);
  dialog.addEventListener("click",event=>{
    const clickedBackdrop=event.target===dialog&&backdropPointerDown;
    backdropPointerDown=false;
    if(clickedBackdrop)dialog.close();
  });
  dialog.querySelector("[data-search-close]")?.addEventListener("click",event=>{
    event.preventDefault();
    if(searchRoute())location.assign("/");else dialog.close();
  });
  dialog.addEventListener("cancel",event=>{
    if(searchRoute()){event.preventDefault();location.replace("/")}
  });
  dialog.addEventListener("close",()=>{
    trigger.setAttribute("aria-expanded","false");
    if(searchRoute()){location.replace("/");return}
    trigger.focus({preventScroll:true});
  });
  const update=source=>results.replaceChildren(...Array.from(source.childNodes,node=>document.importNode(node,true)));
  const search=async (query,version)=>{
    request?.abort();
    request=new AbortController();
    const url=new URL(form.action,location.href);
    url.searchParams.set("q",query);
    try{
      const response=await fetch(url,{headers:{Accept:"text/html"},credentials:"same-origin",signal:request.signal});
      if(!response.ok)throw new Error("search request failed");
      const page=new DOMParser().parseFromString(await response.text(),"text/html");
      const found=page.querySelector("[data-search-results]"),message=page.querySelector("[data-search-status]");
      if(!found||!message)throw new Error("search response is incomplete");
      if(version!==generation)return;
      update(found);status.textContent=message.textContent;
    }catch(error){
      if(version===generation&&error.name!=="AbortError")status.textContent="搜索暂时不可用，请按 Enter 查看搜索结果。";
    }
  };
  input.addEventListener("input",()=>{
    clearTimeout(timer);
    request?.abort();
    const version=++generation;
    const query=input.value.trim();
    if(query.length<2){
      request?.abort();results.replaceChildren();
      status.textContent=query?"请输入至少 2 个字符。":"输入关键词，搜索已发布的文章";
      return;
    }
    status.textContent="正在搜索…";
    timer=setTimeout(()=>search(query,version),220);
  });
  if(dialog.hasAttribute("open")&&typeof dialog.showModal==="function"){
    dialog.removeAttribute("open");
    try{dialog.showModal();trigger.setAttribute("aria-expanded","true");focusInput()}catch{dialog.setAttribute("open","")}
  }
})();
