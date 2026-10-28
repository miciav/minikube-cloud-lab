export async function request<T>(path:string,options:RequestInit={}):Promise<T>{
 const response=await fetch(`/v1/${path}`,{...options,headers:{'Content-Type':'application/json',...options.headers},cache:'no-store'});
 if(!response.ok){const error=await response.json().catch(()=>({error:`Request failed (${response.status})`}));throw Error(error.error||`Request failed (${response.status})`)}
 return response.json();
}

