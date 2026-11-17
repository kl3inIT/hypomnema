document.getElementById("module").textContent = "module script ran"
const data = await (await fetch("data.json")).json()
document.getElementById("data").textContent = data.text
